use super::{NativePreparation, State};

unsafe extern "C" {
    pub(super) fn rh_resident_preparation_request_active_close(handle: u64) -> u32;
    pub(super) fn rh_resident_preparation_discard_observation(handle: u64) -> u32;
    pub(super) fn rh_resident_preparation_exit(handle: u64) -> u64;
    pub(super) fn rh_resident_preparation_retire_active_io(handle: u64) -> u32;
    pub(super) fn rh_resident_preparation_finish_active(handle: u64) -> u32;
    pub(super) fn rh_resident_preparation_abandon_active(handle: u64) -> u32;
}

/// Same-Process observation only. No EOF / close / join / exit permission.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ExitObservation {
    Unavailable,
    Pending,
    Exit(i32),
    Signal(i32),
    Unknown,
}

impl ExitObservation {
    pub(super) fn decode(raw: u64) -> Self {
        let tag = raw >> 32;
        let status = raw as u32 as i32;
        match (tag, status) {
            (0, 0) => Self::Unavailable,
            (1, 0) => Self::Pending,
            (2, 0..=255) => Self::Exit(status),
            (3, 1..=31) => Self::Signal(status),
            _ => Self::Unknown,
        }
    }
}

/// Shutdown discard only; neither bytes nor lifecycle permission are returned.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DiscardObservation {
    Rejected,
    Progress,
    Pending,
    Eof,
    Error,
}

impl DiscardObservation {
    /// Only actual raw EOF completes drain, even when exact Wait already exists.
    pub fn is_eof(self) -> bool {
        self == Self::Eof
    }
}

/// Affine native component, created only by the original owner after finalization.
/// Moving it out of its worker is not proof that that worker actually joined.
#[must_use]
pub struct NativeTerminal {
    _private: (),
}

impl NativePreparation {
    /// One-shot native terminalization. Wire proof and actual joins are separate.
    pub fn finish_active(&mut self) -> Option<NativeTerminal> {
        if !self.active || self.state() != State::ActiveIoRetired {
            return None;
        }
        // SAFETY: private handle and original affine owner; native rechecks TLS,
        // exact completion, all IO receipts and the absence of UNKNOWN history.
        if unsafe { rh_resident_preparation_finish_active(self.handle.get()) } != 7 {
            self.state = State::Unknown;
            return None;
        }
        self.state = State::Terminal;
        self.active = false; // Only this receipt disarms abandonment on Drop.
        Some(NativeTerminal { _private: () })
    }

    pub fn discard_observation(&mut self) -> DiscardObservation {
        if !self.active || self.state != State::Unknown {
            return DiscardObservation::Rejected;
        }
        // SAFETY: private original handle; native validates TLS and shutdown state.
        match unsafe { rh_resident_preparation_discard_observation(self.handle.get()) } {
            0 => DiscardObservation::Rejected,
            1 => DiscardObservation::Progress,
            2 => DiscardObservation::Pending,
            3 => DiscardObservation::Eof,
            _ => DiscardObservation::Error,
        }
    }

    pub fn poll_exit(&mut self) -> ExitObservation {
        if !self.active {
            return ExitObservation::Unavailable;
        }
        // SAFETY: original private handle; native TLS rejects foreign workers.
        let result =
            ExitObservation::decode(unsafe { rh_resident_preparation_exit(self.handle.get()) });
        if matches!(
            result,
            ExitObservation::Unknown | ExitObservation::Unavailable
        ) {
            self.state = State::Unknown;
            return ExitObservation::Unknown;
        }
        result // Unknown IO state is never cleared by a valid exit observation.
    }

    /// Active IO component only. The native record still blocks replacement.
    #[must_use]
    pub fn retire_active_io(&mut self) -> State {
        if !self.active || self.state == State::ActiveIoRetired {
            return self.state;
        }
        // SAFETY: same private owner, native requires its observed exact Wait.
        let raw = unsafe { rh_resident_preparation_retire_active_io(self.handle.get()) };
        self.state = match (self.state, raw) {
            (State::Active, 5) => State::Active,
            (State::Active, 6) => State::ActiveIoRetired,
            _ => State::Unknown,
        };
        self.state
    }

    /// Requests close-once control shutdown without dropping this affine guard.
    /// No receipt, EOF, Wait or join is established; UNKNOWN stays retained.
    pub fn request_active_close(&mut self) {
        if self.active {
            // SAFETY: original private owner; close once without early retirement.
            let _ = unsafe { rh_resident_preparation_request_active_close(self.handle.get()) };
            self.state = State::Unknown;
        }
    }

    pub(super) fn abandon_active(&mut self) {
        // SAFETY: same original handle. Native performs only close-once and a
        // nonblocking completion observation, retaining UNKNOWN unconditionally.
        let _ = unsafe { rh_resident_preparation_abandon_active(self.handle.get()) };
        self.state = State::Unknown;
    }
}

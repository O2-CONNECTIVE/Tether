use super::{NativePreparation, State};

unsafe extern "C" {
    pub(super) fn rh_resident_preparation_exit(handle: u64) -> u64;
    pub(super) fn rh_resident_preparation_retire_active_io(handle: u64) -> u32;
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

impl NativePreparation {
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
            self.abandon_active();
        }
    }

    pub(super) fn abandon_active(&mut self) {
        // SAFETY: same original handle. Native performs only close-once and a
        // nonblocking completion observation, retaining UNKNOWN unconditionally.
        let _ = unsafe { rh_resident_preparation_abandon_active(self.handle.get()) };
        self.state = State::Unknown;
    }
}

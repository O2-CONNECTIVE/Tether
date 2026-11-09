//! Fixed untrusted self-child only. Not part of normal startup or publisher trust.
use super::{ExitObservation, NativePreparation, Preparation, State};
use std::{marker::PhantomData, num::NonZeroU64};

unsafe extern "C" {
    fn rh_fixture_preparation_release_control_gate(handle: u64) -> u32;
    fn rh_fixture_preparation_create_active(mode: u32) -> u64;
    fn rh_fixture_preparation_collect_active() -> u64;
    fn rh_fixture_preparation_active_stats() -> u64;
    fn rh_fixture_preparation_retained_state() -> u32;
}

pub enum FixedActiveFixture {
    Echo = 0,
    Early = 1,
    LostCloseReceipt = 2,
    ExitTwo = 3,
    Signal = 4,
    FiniteMalformed = 5,
    FiniteUnwind = 6,
    FiniteUnknown = 7,
    FiniteHeldUnknown = 8,
    FiniteCoalesced = 9,
    UnprovenExitTwo = 10,
}

#[derive(Debug, PartialEq, Eq)]
pub struct ActiveStats {
    pub waits: u32,
    pub abandons: u32,
}

impl NativePreparation {
    pub fn fixture_active(mode: FixedActiveFixture) -> Preparation {
        // SAFETY: this creates and starts only a fixed test-only self-child,
        // adopting the native record before acquisition/launch can fail.
        let Some(handle) =
            NonZeroU64::new(unsafe { rh_fixture_preparation_create_active(mode as u32) })
        else {
            return Preparation::Refused;
        };
        // SAFETY: original same-thread native handle, never a caller assertion.
        let state = if unsafe { super::rh_resident_preparation_state(handle.get()) } == 5 {
            State::Active
        } else {
            State::Unknown
        };
        let owner = Self {
            handle,
            state,
            active: true,
            original_thread: PhantomData,
        };
        if state == State::Active {
            Preparation::Owned(owner)
        } else {
            Preparation::Unknown(owner)
        }
    }

    pub fn fixture_active_stats() -> ActiveStats {
        // SAFETY: reads only fixture counters under the slot lock.
        let raw = unsafe { rh_fixture_preparation_active_stats() };
        ActiveStats {
            waits: (raw >> 32) as u32,
            abandons: raw as u32,
        }
    }

    pub fn fixture_collect_retained_active() -> ExitObservation {
        // SAFETY: no replacement handle; only the original retained record and
        // original TLS worker can collect its fixed child. UNKNOWN never clears.
        ExitObservation::decode(unsafe { rh_fixture_preparation_collect_active() })
    }
}

impl NativePreparation {
    pub fn fixture_retained_state() -> u32 {
        // SAFETY: reads only locked slot metadata, never a foreign Process.
        unsafe { rh_fixture_preparation_retained_state() }
    }
    pub fn fixture_foreign_active_calls(&self) -> bool {
        let handle = self.handle.get();
        std::thread::spawn(move || {
            // SAFETY: intentional foreign-thread ABI negatives. Native must
            // reject before any original Process/IO operation. No owner moves.
            unsafe {
                super::active::rh_resident_preparation_exit(handle) == 5_u64 << 32
                    && super::active::rh_resident_preparation_retire_active_io(handle) == 4
                    && super::active::rh_resident_preparation_finish_active(handle) == 4
                    && super::active::rh_resident_preparation_abandon_active(handle) == 4
                    && super::active::rh_resident_preparation_request_active_close(handle) == 4
                    && super::active::rh_resident_preparation_discard_observation(handle) == 0
            }
        })
        .join()
        .expect("foreign probe thread")
    }
    pub fn fixture_reject_prelaunch_active_calls(&self) -> bool {
        // SAFETY: same real unlaunched native handle; reject active operations.
        unsafe {
            super::active::rh_resident_preparation_discard_observation(self.handle.get()) == 0
                && super::active::rh_resident_preparation_request_active_close(self.handle.get())
                    == 1
                && super::active::rh_resident_preparation_finish_active(self.handle.get()) == 0
                && super::active::rh_resident_preparation_exit(self.handle.get()) == 0
                && super::active::rh_resident_preparation_retire_active_io(self.handle.get()) == 1
                && super::active::rh_resident_preparation_abandon_active(self.handle.get()) == 1
        }
    }
}
impl ExitObservation {
    // Test-only packed ABI parser seam; observations grant no lifecycle rights.
    pub fn fixture_decode(raw: u64) -> Self {
        Self::decode(raw)
    }
}

impl NativePreparation {
    pub fn fixture_release_control_gate(&mut self) -> bool {
        // SAFETY: same private native owner/TLS; closes only its fixed fixture gate.
        unsafe { rh_fixture_preparation_release_control_gate(self.handle.get()) == 1 }
    }
    pub fn fixture_discard_rejected(&self) -> bool {
        // SAFETY: exact original handle; this fixture checks the native gate too.
        unsafe {
            super::active::rh_resident_preparation_discard_observation(self.handle.get()) == 0
        }
    }
    pub fn fixture_unknown_bytes_rejected(&self) -> bool {
        let mut byte = 0_u8;
        // SAFETY: live one-byte buffers; direct ABI negatives cannot bypass UNKNOWN.
        unsafe {
            super::byte_io::rh_resident_preparation_read(self.handle.get(), &mut byte, 1) == 0
                && super::byte_io::rh_resident_preparation_write(self.handle.get(), &byte, 1) == 0
                && super::active::rh_resident_preparation_discard_observation(0) == 0
                && super::active::rh_resident_preparation_request_active_close(0) == 0
        }
    }
}

impl NativePreparation {
    pub fn fixture_finish_rejected(&self) -> bool {
        // SAFETY: same original handle, deliberately before/after valid one-shot
        // finalization. A rejected call must not alter the native record.
        unsafe { super::active::rh_resident_preparation_finish_active(self.handle.get()) == 0 }
    }
}

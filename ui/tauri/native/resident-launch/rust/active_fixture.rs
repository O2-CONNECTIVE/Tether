//! Fixed untrusted self-child only. Not part of normal startup or publisher trust.
use super::{ExitObservation, NativePreparation, Preparation, State};
use std::{marker::PhantomData, num::NonZeroU64};

unsafe extern "C" {
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
                    && super::active::rh_resident_preparation_abandon_active(handle) == 4
            }
        })
        .join()
        .expect("foreign probe thread")
    }
    pub fn fixture_reject_prelaunch_active_calls(&self) -> bool {
        // SAFETY: same real unlaunched native handle; reject active operations.
        unsafe {
            super::active::rh_resident_preparation_exit(self.handle.get()) == 0
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

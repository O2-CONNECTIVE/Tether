//! Compiled only for the explicit native source fixture, never normal startup.
use super::{NativePreparation, Preparation};

unsafe extern "C" {
    fn rh_fixture_preparation_create(mode: u32) -> u64;
    fn rh_fixture_preparation_probe(handle: u64) -> u32;
    fn rh_fixture_preparation_exchange(handle: u64) -> u32;
    fn rh_fixture_preparation_close_count() -> u32;
    fn rh_fixture_preparation_open_count() -> u32;
    fn rh_fixture_preparation_retire_calls() -> u32;
}

pub enum FixedFixture {
    Normal = 0,
    LostCloseReceipt = 1,
    PartialAcquisition = 2,
    PartialLostReceipt = 3,
}

#[derive(Debug, PartialEq, Eq)]
pub struct Counts {
    pub open: u32,
    pub closes: u32,
    pub retire_calls: u32,
}

impl NativePreparation {
    pub fn fixture(mode: FixedFixture) -> Preparation {
        // SAFETY: fixed test-only mode; returned handle belongs to this thread.
        Self::adopt(unsafe { rh_fixture_preparation_create(mode as u32) })
    }

    pub fn fixture_probe(&self) -> bool {
        // SAFETY: same private native handle, no raw pointer or fd crosses ABI.
        unsafe { rh_fixture_preparation_probe(self.handle.get()) == 31 }
    }

    pub fn fixture_exchange(&mut self) -> bool {
        // SAFETY: native fixture owns both pipe ends; its IO is nonblocking.
        unsafe { rh_fixture_preparation_exchange(self.handle.get()) == 1 }
    }

    pub fn fixture_native_state(&self) -> u32 {
        // SAFETY: stale handles are rejected by the real native registry.
        unsafe { super::rh_resident_preparation_state(self.handle.get()) }
    }

    pub fn fixture_counts() -> Counts {
        // SAFETY: these native counters are read under the native slot lock.
        unsafe {
            Counts {
                open: rh_fixture_preparation_open_count(),
                closes: rh_fixture_preparation_close_count(),
                retire_calls: rh_fixture_preparation_retire_calls(),
            }
        }
    }
}

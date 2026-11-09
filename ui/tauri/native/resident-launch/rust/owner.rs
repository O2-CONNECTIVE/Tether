//! Concrete client of ResidentPreparation.h. Not linked into normal Tauri.
//! Every native operation uses the same non-transferable owner; no launch API.
use std::{marker::PhantomData, num::NonZeroU64, rc::Rc};

#[cfg(resident_launch_fixture)]
mod fixture;
#[cfg(resident_launch_fixture)]
pub use fixture::{Counts, FixedFixture};
mod active;
pub type DiscardObservation = active::DiscardObservation;
pub type ExitObservation = active::ExitObservation;
pub type NativeTerminal = active::NativeTerminal;
#[cfg(resident_launch_fixture)]
mod active_fixture;
#[cfg(resident_launch_fixture)]
pub type ActiveStats = active_fixture::ActiveStats;
#[cfg(resident_launch_fixture)]
pub type FixedActiveFixture = active_fixture::FixedActiveFixture;
mod byte_io;
pub use byte_io::PreparedIo;

unsafe extern "C" {
    fn rh_resident_preparation_create() -> u64;
    fn rh_resident_preparation_state(handle: u64) -> u32;
    fn rh_resident_preparation_retire(handle: u64) -> u32;
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum State {
    OwnedUnlaunched,
    RetiredUnlaunched,
    Active,
    ActiveIoRetired,
    Terminal,
    Unknown,
}

#[must_use = "inspect refusal or the retained owner's state"]
pub enum Preparation {
    Refused,
    Owned(NativePreparation),
    Unknown(NativePreparation),
}

#[must_use = "retain the original owner until retirement is established"]
pub struct NativePreparation {
    handle: NonZeroU64,
    state: State,
    active: bool,
    // Both Send and Sync are forbidden, including for shared references.
    original_thread: PhantomData<Rc<()>>,
}

impl NativePreparation {
    pub fn prepare() -> Preparation {
        // SAFETY: no inputs; native create is the only source of handles.
        Self::adopt(unsafe { rh_resident_preparation_create() })
    }

    fn adopt(raw: u64) -> Preparation {
        let Some(handle) = NonZeroU64::new(raw) else {
            return Preparation::Refused;
        };
        // SAFETY: handle came from native create on this still-current thread.
        let state = if unsafe { rh_resident_preparation_state(handle.get()) } == 1 {
            State::OwnedUnlaunched
        } else {
            State::Unknown
        };
        let owner = Self {
            handle,
            state,
            active: false,
            original_thread: PhantomData,
        };
        if state == State::OwnedUnlaunched {
            Preparation::Owned(owner)
        } else {
            Preparation::Unknown(owner)
        }
    }

    pub fn state(&mut self) -> State {
        let expected = match self.state {
            State::OwnedUnlaunched => 1,
            State::Active => 5,
            State::ActiveIoRetired => 6,
            State::Terminal => 7,
            _ => return self.state,
        };
        // SAFETY: private handle, same thread enforced by !Send/!Sync and TLS.
        if unsafe { rh_resident_preparation_state(self.handle.get()) } != expected {
            self.state = State::Unknown;
        }
        self.state
    }

    /// Only retirement of UNLAUNCHED preparation; never an exit/Wait proof.
    #[must_use]
    pub fn retire(&mut self) -> State {
        if self.state == State::OwnedUnlaunched {
            // SAFETY: same owned handle, at most one retirement call per guard.
            self.state = if unsafe { rh_resident_preparation_retire(self.handle.get()) } == 2 {
                State::RetiredUnlaunched
            } else {
                State::Unknown
            };
        }
        self.state
    }
}

impl Drop for NativePreparation {
    fn drop(&mut self) {
        // No success notification, replacement or second close after unknown.
        // The native slot retains the original Process on any uncertain result.
        if self.active {
            self.abandon_active();
        } else {
            let _ = self.retire();
        }
    }
}

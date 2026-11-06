//! Concrete prepared native IO, compiled only in an explicit source variant.
use super::*;
use crate::native_preparation::{NativePreparation, PreparedIo, State};
use std::sync::{Arc, Mutex};

impl<'owner> Pump<PreparedIo<'owner>> {
    pub(in crate::resident_port::duplex) fn take_active(
        owner: &'owner mut NativePreparation,
    ) -> Result<Self, ()> {
        if owner.state() != State::Active {
            return Err(());
        }
        Ok(Self::with_io(owner.io()))
    }

    /// Borrows the original thread-confined owner through the shared IO loop.
    /// Returning proves neither EOF, original Process Wait, receipts nor joins.
    pub(in crate::resident_port::duplex) fn drive_active_io(
        owner: &'owner mut NativePreparation,
        clock: ContinuousClock,
        shared: Arc<Mutex<Shared>>,
    ) -> Result<(), ()> {
        let mut pump = Self::take_active(owner)?;
        super::super::owner::drive_io(|state, clock| pump.step(state, clock), clock, shared);
        Ok(())
    }

    pub(in crate::resident_port::duplex) fn take_prepared(
        owner: &'owner mut NativePreparation,
    ) -> Result<Self, ()> {
        if owner.state() != State::OwnedUnlaunched {
            return Err(());
        }
        Ok(Self::with_io(owner.io()))
    }

    pub(in crate::resident_port::duplex) fn step(
        &mut self,
        shared: &mut Shared,
        clock: &ContinuousClock,
    ) -> Result<bool, ()> {
        let now = clock.now_millis().ok_or(())?;
        if shared.closure.requested() && !self.control_closed {
            if self.io.half_close_control().is_err() {
                shared.fault();
                self.discard = true;
            } else {
                self.control_closed = true;
                shared.closure.half_closed();
            }
        }
        self.step_io(shared, clock, now)
    }

    #[cfg(resident_launch_fixture)]
    pub(in crate::resident_port::duplex) fn fixture_send(&mut self, bytes: &[u8]) -> usize {
        self.io.fixture_send(bytes)
    }
    #[cfg(resident_launch_fixture)]
    pub(in crate::resident_port::duplex) fn fixture_receive(&mut self, bytes: &mut [u8]) -> usize {
        self.io.fixture_receive(bytes)
    }
    #[cfg(resident_launch_fixture)]
    pub(in crate::resident_port::duplex) fn fixture_peer(&mut self, action: u32) -> bool {
        self.io.fixture_peer(action)
    }
}

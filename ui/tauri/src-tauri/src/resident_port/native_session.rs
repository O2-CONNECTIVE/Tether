//! Same closed native preparation and its typed port; no production activation.
use super::{
    duplex::owner::{native::NativeIoSupervisor, BoundDuplex},
    ResidentPort,
};
use crate::standby_lifecycle::native::NativeOuterOwner;
use std::io;

pub(crate) struct NativeSession {
    // Private: callers can borrow views, never move the port away from its owner.
    port: ResidentPort,
    owner: NativeOuterOwner,
}
impl NativeSession {
    pub(crate) fn prepare() -> io::Result<Self> {
        Self::attach(NativeIoSupervisor::prepare()?)
    }
    fn attach(
        (producer, source): (NativeIoSupervisor, io::Result<BoundDuplex>),
    ) -> io::Result<Self> {
        // Establish outer ownership before waiting for any first observation.
        let owner = NativeOuterOwner::start(producer)?;
        let port = source.and_then(ResidentPort::bound).unwrap_or_else(|_| {
            // Attachment failure is unavailable, with this SAME owner retained.
            // Close seals submissions; it never authorizes Quit or replacement.
            let _ = owner.request_exit();
            ResidentPort::default()
        });
        Ok(Self { port, owner })
    }
    pub(crate) fn port(&self) -> &ResidentPort {
        &self.port
    }
    pub(crate) fn owner(&self) -> &NativeOuterOwner {
        &self.owner
    }
    #[cfg(resident_launch_fixture)]
    pub(crate) fn fixture(
        mode: crate::native_preparation::FixedActiveFixture,
        unwind: bool,
    ) -> io::Result<Self> {
        Self::attach(NativeIoSupervisor::fixture(mode, unwind)?)
    }
}
// No custom Drop: private port drops first, then original outer owner's guard.
// Dropping/losing a viewer is only a borrow and never closes its control pipe.

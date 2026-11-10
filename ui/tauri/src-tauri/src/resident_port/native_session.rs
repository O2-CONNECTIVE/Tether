//! Same closed native startup and its typed port; release identity is required.
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
        let owner = match NativeOuterOwner::start(producer) {
            Ok(owner) => owner,
            Err(_) => retain_failed_monitor(),
        };
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

// Returning Err here would let GUI setup panic/fall back despite ownership
// having existed. Keep this explicitly diverging, including after worker join.
fn retain_failed_monitor() -> ! {
    #[cfg(resident_launch_fixture)]
    NativeOuterOwner::fixture_observe_failure_containment();
    loop {
        std::thread::park_timeout(std::time::Duration::from_secs(1));
    }
}

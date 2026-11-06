//! Test-only Command factory; ordinary construction remains private and unbound.
pub(crate) use super::launcher::Attachment;
use super::*;
impl Supervisor {
    pub(crate) fn fixture_retained(
        command: std::process::Command,
    ) -> io::Result<(Self, io::Result<BoundDuplex>)> {
        Self::start(command, true, Attachment::Normal)
    }
    pub(crate) fn fixture(
        command: std::process::Command,
    ) -> io::Result<(Self, io::Result<BoundDuplex>)> {
        Self::start(command, false, Attachment::Normal)
    }
    pub(crate) fn fixture_with(
        command: std::process::Command,
        fault: Attachment,
    ) -> io::Result<(Self, io::Result<BoundDuplex>)> {
        Self::start(command, false, fault)
    }
}

// Reuse the same harmless owned pipe fixture at the concrete outer-owner seam.
impl Supervisor {
    pub(crate) fn close_fixture(mode: &str) -> (Self, BoundDuplex) {
        let (owner, source) =
            Self::fixture(super::super::owned_close_tests::command(mode)).unwrap();
        (owner, source.unwrap())
    }
}

impl CloseHandle {
    // State-only gate negatives allocate no process or worker.
    pub(crate) fn fixture() -> Self {
        Self(Arc::new(Mutex::new(Shared::default())))
    }
}

impl Supervisor {
    pub(crate) fn retained_close_fixture() -> (Self, BoundDuplex) {
        let (owner, source) =
            Self::fixture_retained(super::super::owned_close_tests::command("confirmed")).unwrap();
        (owner, source.unwrap())
    }
}

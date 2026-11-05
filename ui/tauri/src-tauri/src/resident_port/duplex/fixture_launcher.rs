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

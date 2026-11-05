//! Owned Mac FIFO prerequisite, not a trusted-source authenticator.
//! The caller must transfer a dedicated, explicitly composed read descriptor.
use super::continuous_clock::ContinuousClock;
use super::provider::{Input, Provider, ReadStep};
use std::fs::File;
use std::io::{ErrorKind, Read};
use std::os::fd::AsRawFd;
use std::os::unix::fs::FileTypeExt;

// Verified against the installed macOS SDK sys/fcntl.h.
const F_GETFD: i32 = 1;
const F_SETFD: i32 = 2;
const F_GETFL: i32 = 3;
const F_SETFL: i32 = 4;
const FD_CLOEXEC: i32 = 1;
const O_ACCMODE: i32 = 3;
const O_NONBLOCK: i32 = 4;

unsafe extern "C" {
    fn fcntl(fd: i32, command: i32, ...) -> i32;
}

pub(crate) struct NativeInput {
    file: File,
}
impl NativeInput {
    // Private native factory only. No startup or IPC route invokes it yet.
    pub(crate) fn source(file: File) -> Option<Provider<Self, impl FnMut() -> Option<f64>>> {
        let clock = ContinuousClock::new()?;
        let input = Self::from_owned_fifo(file).ok()?;
        Provider::new(input, move || clock.now_seconds())
    }

    // Taking File transfers ownership; failure closes only that owned handle.
    // O_NONBLOCK affects its open-file description, so shared/unowned input is forbidden.
    pub(crate) fn from_owned_fifo(file: File) -> Result<Self, ()> {
        if !file.metadata().map_err(|_| ())?.file_type().is_fifo() {
            return Err(());
        }
        let fd = file.as_raw_fd();
        // SAFETY: File owns a live descriptor; GET calls take no variadic argument.
        let flags = unsafe { fcntl(fd, F_GETFL) };
        let descriptor_flags = unsafe { fcntl(fd, F_GETFD) };
        if flags < 0 || descriptor_flags < 0 || flags & O_ACCMODE != 0 {
            return Err(());
        }
        // SAFETY: SET calls receive the required integer flag argument.
        if unsafe { fcntl(fd, F_SETFD, descriptor_flags | FD_CLOEXEC) } < 0
            || unsafe { fcntl(fd, F_SETFL, flags | O_NONBLOCK) } < 0
        {
            return Err(());
        }
        Ok(Self { file })
    }
}
impl Input for NativeInput {
    fn read_once(&mut self, bytes: &mut [u8; 4096]) -> Result<ReadStep, ()> {
        match self.file.read(bytes) {
            Ok(0) => Ok(ReadStep::Eof),
            Ok(n) => Ok(ReadStep::Bytes(n)),
            Err(e) if matches!(e.kind(), ErrorKind::WouldBlock | ErrorKind::Interrupted) => {
                Ok(ReadStep::Pending)
            }
            Err(_) => Err(()),
        }
    }
}

#[cfg(test)]
#[path = "native_input_tests.rs"]
mod tests;

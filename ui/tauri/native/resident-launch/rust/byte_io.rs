use super::{NativePreparation, State};
use std::io::{self, ErrorKind, Read, Write};

unsafe extern "C" {
    pub(super) fn rh_resident_preparation_read(handle: u64, bytes: *mut u8, count: u32) -> u64;
    pub(super) fn rh_resident_preparation_write(handle: u64, bytes: *const u8, count: u32) -> u64;
    fn rh_resident_preparation_half_close(handle: u64) -> u64;
}

/// Borrowed prepared pipes, not authenticated observation or active child IO.
pub struct PreparedIo<'owner> {
    owner: &'owner mut NativePreparation,
}

impl NativePreparation {
    pub fn io(&mut self) -> PreparedIo<'_> {
        PreparedIo { owner: self }
    }
}

impl PreparedIo<'_> {
    #[cfg(resident_launch_fixture)]
    pub fn fixture_send(&mut self, bytes: &[u8]) -> usize {
        self.owner.fixture_send(bytes)
    }
    #[cfg(resident_launch_fixture)]
    pub fn fixture_receive(&mut self, bytes: &mut [u8]) -> usize {
        self.owner.fixture_receive(bytes)
    }
    #[cfg(resident_launch_fixture)]
    pub fn fixture_peer(&mut self, action: u32) -> bool {
        self.owner.fixture_peer(action)
    }
    fn available(&self) -> io::Result<()> {
        if self.owner.state == State::OwnedUnlaunched {
            Ok(())
        } else {
            Err(io::Error::from(ErrorKind::NotConnected))
        }
    }

    fn unknown<T>(&mut self) -> io::Result<T> {
        self.owner.state = State::Unknown;
        Err(io::Error::other("NATIVE_PREPARED_IO_UNKNOWN"))
    }

    /// Half-close only the owned control writer. Not an OFF/Close/Wait proof.
    pub fn half_close_control(&mut self) -> io::Result<()> {
        self.available()?;
        // SAFETY: private live handle; exclusive borrow retains the same owner.
        let result = unsafe { rh_resident_preparation_half_close(self.owner.handle.get()) };
        if result == 4_u64 << 32 {
            Ok(())
        } else {
            self.unknown()
        }
    }

    fn transferred(&mut self, result: u64, limit: usize, read: bool) -> io::Result<usize> {
        let tag = result >> 32;
        let count = (result & 0xffff_ffff) as usize;
        match (tag, count) {
            (1, n) if n > 0 && n <= limit => Ok(n),
            (2, 0) => Err(io::Error::from(ErrorKind::WouldBlock)),
            (3, 0) if read => Ok(0), // Raw pipe EOF; no lifecycle state change.
            (4, 0) if !read => Err(io::Error::from(ErrorKind::BrokenPipe)),
            _ => self.unknown(),
        }
    }
}

impl Read for PreparedIo<'_> {
    fn read(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
        self.available()?;
        if bytes.is_empty() {
            return Ok(0); // Empty read is not evidence of EOF.
        }
        let limit = bytes.len().min(4096);
        // SAFETY: slice is live/writable for limit bytes; native retains no
        // pointer and accesses only its own nonblocking fd on this thread.
        let result = unsafe {
            rh_resident_preparation_read(self.owner.handle.get(), bytes.as_mut_ptr(), limit as u32)
        };
        self.transferred(result, limit, true)
    }
}

impl Write for PreparedIo<'_> {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.available()?;
        if bytes.is_empty() {
            return Ok(0);
        }
        let limit = bytes.len().min(4096);
        // SAFETY: live readable slice; same owner, no retained buffer pointer.
        let result = unsafe {
            rh_resident_preparation_write(self.owner.handle.get(), bytes.as_ptr(), limit as u32)
        };
        self.transferred(result, limit, false)
    }

    fn flush(&mut self) -> io::Result<()> {
        // No userspace output buffer. This is not a receipt or delivery proof.
        self.available()
    }
}

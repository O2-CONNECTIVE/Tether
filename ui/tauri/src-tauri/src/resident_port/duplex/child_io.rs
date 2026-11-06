//! Dedicated handles taken only from the same exact std Child.
use std::fs::File;
use std::io::{self, Read, Write};
use std::os::fd::{AsRawFd, OwnedFd};
use std::process::Child;

pub(crate) struct ChildIo {
    input: File,
    output: Option<File>,
}
impl ChildIo {
    pub(super) fn take(child: &mut Child) -> Result<Self, ()> {
        let input = File::from(OwnedFd::from(child.stdout.take().ok_or(())?));
        let output = File::from(OwnedFd::from(child.stdin.take().ok_or(())?));
        configure(&input, 0)?;
        configure(&output, 1)?;
        Ok(Self {
            input,
            output: Some(output),
        })
    }
    pub(super) fn half_close(&mut self) {
        drop(self.output.take());
    }
}
impl Read for ChildIo {
    fn read(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
        self.input.read(bytes)
    }
}
impl Write for ChildIo {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.output
            .as_mut()
            .ok_or_else(|| io::Error::from(io::ErrorKind::NotConnected))?
            .write(bytes)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.output
            .as_mut()
            .ok_or_else(|| io::Error::from(io::ErrorKind::NotConnected))?
            .flush()
    }
}
fn configure(file: &File, access: i32) -> Result<(), ()> {
    use std::os::unix::fs::FileTypeExt;
    if !file.metadata().map_err(|_| ())?.file_type().is_fifo() {
        return Err(());
    }
    let fd = file.as_raw_fd();
    // macOS SDK: GETFD1/SETFD2/GETFL3/SETFL4, CLOEXEC1/ACCMODE3/NONBLOCK4.
    unsafe extern "C" {
        fn fcntl(fd: i32, command: i32, ...) -> i32;
    }
    // SAFETY: owned live descriptors and SDK's required variadic integer types.
    unsafe {
        let flags = fcntl(fd, 3);
        let descriptor = fcntl(fd, 1);
        if flags < 0
            || descriptor < 0
            || flags & 3 != access
            || fcntl(fd, 2, descriptor | 1) < 0
            || fcntl(fd, 4, flags | 4) < 0
        {
            return Err(());
        }
    }
    Ok(())
}

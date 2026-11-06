//! Bounded nonblocking IO on the exact child's dedicated handles.
use super::{exchange::Shared, wire};
use crate::mac_status::ContinuousClock;
use std::fs::File;
use std::io::{ErrorKind, Read, Write};
use std::os::fd::{AsRawFd, OwnedFd};
use std::process::Child;
pub(super) struct Pump {
    input: File,
    output: Option<File>,
    line: Vec<u8>,
    line_started: Option<u64>,
    discard: bool,
}
impl Pump {
    pub(super) fn take(child: &mut Child) -> Result<Self, ()> {
        // Same exact Child, not supplied File/fd. FIFO is NOT authentication.
        let input = File::from(OwnedFd::from(child.stdout.take().ok_or(())?));
        let output = File::from(OwnedFd::from(child.stdin.take().ok_or(())?));
        configure(&input, 0)?;
        configure(&output, 1)?;
        Ok(Self {
            input,
            output: Some(output),
            line: Vec::with_capacity(wire::MAX_LINE),
            line_started: None,
            discard: false,
        })
    }
    pub(super) fn step(
        &mut self,
        shared: &mut Shared,
        clock: &ContinuousClock,
    ) -> Result<bool, ()> {
        let now = clock.now_millis().ok_or(())?;
        if shared.closure.requested() && self.output.is_some() {
            drop(self.output.take()); // Deliberate half-close, exactly once.
            shared.closure.half_closed();
        }
        if self.discard {
            return self.read_discard(shared);
        }
        let result = self.step_checked(shared, now, clock);
        if result.is_err() && (shared.closure.requested() || shared.startup.received()) {
            // Keep draining bounded chunks after invalid shutdown evidence. A
            // producer's final write must not deadlock because our parser quit.
            shared.fault();
            self.line.clear();
            self.discard = true;
            return Ok(false);
        }
        result
    }
    fn step_checked(
        &mut self,
        shared: &mut Shared,
        now: u64,
        clock: &ContinuousClock,
    ) -> Result<bool, ()> {
        shared.sample(now)?;
        self.check_partial(now)?;
        if !shared.closure.requested()
            && (shared.tracking.failed
                || shared
                    .waiting
                    .as_ref()
                    .is_some_and(|(_, _, until)| now >= *until))
        {
            return Err(());
        }
        if let Some(bytes) = &shared.output {
            let result = self.output.as_mut().ok_or(())?.write(bytes);
            shared.written(result)?;
        }
        let mut bytes = [0; 1024];
        let read = self.input.read(&mut bytes);
        // Producer can timestamp/write between our earlier sample and read.
        // Compare captured bytes against actual time sampled AFTER the read.
        let now = clock.now_millis().ok_or(())?;
        shared.sample(now)?;
        self.check_partial(now)?;
        if !shared.closure.requested()
            && shared
                .waiting
                .as_ref()
                .is_some_and(|(_, _, until)| now >= *until)
        {
            return Err(());
        }
        match read {
            Ok(0) => {
                if !self.line.is_empty() {
                    return Err(());
                }
                shared.ended();
                Ok(true)
            }
            Ok(n) => {
                self.consume(shared, &bytes[..n], now)?;
                Ok(false)
            }
            Err(e) if matches!(e.kind(), ErrorKind::WouldBlock | ErrorKind::Interrupted) => {
                Ok(false)
            }
            Err(_) => Err(()),
        }
    }
    fn check_partial(&self, now: u64) -> Result<(), ()> {
        if self
            .line_started
            .is_some_and(|start| now < start || now - start >= 1000)
        {
            Err(())
        } else {
            Ok(())
        }
    }
    fn consume(&mut self, shared: &mut Shared, bytes: &[u8], now: u64) -> Result<(), ()> {
        self.check_partial(now)?;
        for byte in bytes {
            if *byte == b'\n' {
                let event = wire::Event::parse(&self.line)?;
                self.line.clear();
                self.line_started = None;
                if shared.startup.event(&event, now)? {
                    continue;
                }
                if shared.closure.requested() {
                    shared.closure.event(event, now)?;
                } else if let Some(receipt) = shared.tracking.event(event, now)? {
                    if shared.output.is_some() {
                        return Err(());
                    }
                    let (_, send, _) = shared.waiting.take().ok_or(())?;
                    let _ = send.try_send(receipt);
                }
            } else {
                if self.line.len() == wire::MAX_LINE {
                    return Err(());
                }
                if self.line.is_empty() {
                    self.line_started = Some(now);
                }
                self.line.push(*byte);
            }
        }
        Ok(())
    }
    fn read_discard(&mut self, shared: &mut Shared) -> Result<bool, ()> {
        let mut bytes = [0; 1024];
        match self.input.read(&mut bytes) {
            Ok(0) => {
                shared.ended();
                Ok(true)
            }
            Ok(_) => Ok(false),
            Err(e) if matches!(e.kind(), ErrorKind::WouldBlock | ErrorKind::Interrupted) => {
                Ok(false)
            }
            Err(_) => Err(()),
        }
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

#[cfg(test)]
#[path = "timing_tests.rs"]
mod timing_tests;

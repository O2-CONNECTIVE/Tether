use super::decoder::MAX_LINE_BYTES;
use super::state::{FeedFault, FeedState, Snapshot};

const READ_BYTES: usize = 4096;
const BUFFER_BYTES: usize = MAX_LINE_BYTES + READ_BYTES;

// Memory-only model of PipeReader's read chunks. This does not own a pipe,
// descriptor, thread or clock. The future caller must supply continuous time,
// including sleep, and positive chunks from a read of at most4096 bytes.
pub(crate) struct Framing {
    state: FeedState,
    buffer: [u8; BUFFER_BYTES],
    len: usize,
    rate_start: f64,
    frames: usize,
    finished: bool,
}

impl Framing {
    pub(crate) fn new(now: f64) -> Self {
        Self {
            state: FeedState::default(),
            buffer: [0; BUFFER_BYTES],
            len: 0,
            rate_start: now,
            frames: 0,
            finished: false,
        }
    }

    pub(crate) fn snapshot(&self) -> &Snapshot {
        self.state.snapshot()
    }

    // Clock is called once per complete line, as in Swift. No OS clock adapter
    // is installed here; fake closures drive all candidate tests.
    pub(crate) fn push_read(&mut self, bytes: &[u8], mut now: impl FnMut() -> f64) {
        if self.finished {
            return;
        }
        if bytes.is_empty() || bytes.len() > READ_BYTES {
            // Caller contract violation, not a new wire rule. EOF has its own
            // operation and only actual <=4096-byte read chunks are modeled.
            self.finish(FeedFault::Read);
            return;
        }
        self.buffer[self.len..self.len + bytes.len()].copy_from_slice(bytes);
        self.len += bytes.len();
        while let Some(end) = self.buffer[..self.len].iter().position(|byte| *byte == b'\n') {
            let time = now();
            if time - self.rate_start >= 1.0 {
                self.rate_start = time;
                self.frames = 0;
            }
            self.frames += 1;
            if self.frames > 64 {
                self.finish(FeedFault::Rate);
                return;
            }
            self.state.ingest(&self.buffer[..end], time);
            self.buffer.copy_within(end + 1..self.len, 0);
            self.len -= end + 1;
            if self.state.snapshot().fault.is_some() {
                self.finished = true;
                return;
            }
        }
        if self.len > MAX_LINE_BYTES {
            self.finish(FeedFault::Malformed);
        }
    }

    pub(crate) fn eof(&mut self) {
        self.finish(if self.len == 0 { FeedFault::Eof } else { FeedFault::Malformed });
    }

    pub(crate) fn finish(&mut self, fault: FeedFault) {
        if !self.finished {
            self.state.fail(fault);
            self.finished = true;
        }
    }
}

#[cfg(test)]
#[path = "framing_tests.rs"]
mod tests;

use super::framing::Framing;
use super::state::{FeedFault, Snapshot};

pub(crate) enum ReadStep {
    Pending,
    Bytes(usize),
    Eof,
}
pub(crate) trait Input {
    fn read_once(&mut self, bytes: &mut [u8; 4096]) -> Result<ReadStep, ()>;
}

// One input lifetime, no reopen/reset and no constructor from UI data.
pub(crate) struct Provider<R: Input, C: FnMut() -> Option<f64>> {
    input: Option<R>,
    clock: C,
    feed: Framing,
    last_time: f64,
}

impl<R: Input, C: FnMut() -> Option<f64>> Provider<R, C> {
    pub(crate) fn new(input: R, mut clock: C) -> Option<Self> {
        let now = clock().filter(|v| v.is_finite() && *v >= 0.0)?;
        Some(Self {
            input: Some(input),
            clock,
            feed: Framing::new(now),
            last_time: now,
        })
    }

    fn time(&mut self) -> Option<f64> {
        let now = (self.clock)().filter(|v| v.is_finite() && *v >= self.last_time);
        if let Some(now) = now {
            self.last_time = now;
        } else {
            self.finish(FeedFault::Clock);
        }
        now
    }

    fn finish(&mut self, fault: FeedFault) {
        self.feed.finish(fault);
        self.input = None;
    }

    // Bounded nonblocking pump, to be called by a separately owned lifecycle.
    // It is not attached to Tauri IPC or a background worker in this slice.
    pub(crate) fn pump_once(&mut self) {
        if self.input.is_none() {
            return;
        }
        if self.time().is_none() {
            return;
        }
        let mut bytes = [0; 4096];
        let result = self.input.as_mut().unwrap().read_once(&mut bytes);
        match result {
            Ok(ReadStep::Pending) => {}
            Ok(ReadStep::Eof) => {
                self.feed.eof();
                self.input = None;
            }
            Err(()) => self.finish(FeedFault::Read),
            Ok(ReadStep::Bytes(n)) if n == 0 || n > bytes.len() => self.finish(FeedFault::Read),
            Ok(ReadStep::Bytes(n)) => {
                let mut last = self.last_time;
                self.feed.push_read(&bytes[..n], || {
                    match (self.clock)().filter(|v| v.is_finite() && *v >= last) {
                        Some(now) => {
                            last = now;
                            now
                        }
                        None => f64::NAN,
                    }
                });
                self.last_time = last;
                if self
                    .feed
                    .snapshot()
                    .fault
                    .is_some_and(|f| f != FeedFault::Unavailable)
                {
                    self.input = None;
                }
            }
        }
    }

    pub(crate) fn observation(&mut self) -> (Snapshot, Option<f64>) {
        let now = self.time();
        (self.feed.snapshot().clone(), now)
    }

    pub(crate) fn ended(&self) -> bool {
        self.input.is_none()
    }

    pub(crate) fn stop(&mut self) {
        self.finish(FeedFault::Stopped);
    }
}

#[cfg(test)]
#[path = "provider_tests.rs"]
mod tests;

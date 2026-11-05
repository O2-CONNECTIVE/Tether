//! Only the enclosing exact-child attachment can construct this viewer handle.
use super::super::continuous_clock::ContinuousClock;
use super::super::reader_owner::ReaderOwner;
use super::super::state::{FeedFault, Snapshot};
use std::sync::{Arc, Mutex};

pub(crate) struct BoundSource {
    reader: Arc<Mutex<ReaderOwner>>,
    clock: ContinuousClock,
}
impl BoundSource {
    pub(super) fn from_launcher(reader: Arc<Mutex<ReaderOwner>>, clock: ContinuousClock) -> Self {
        Self { reader, clock }
    }
    pub(crate) fn observation(&self) -> (Snapshot, f64) {
        let reader = self.reader.lock().unwrap_or_else(|e| e.into_inner());
        let mut snapshot = reader.snapshot();
        let now = self.clock.now_seconds().unwrap_or_else(|| {
            reader.request_stop();
            if snapshot.fault.is_none() || snapshot.fault == Some(FeedFault::Unavailable) {
                snapshot.fault = Some(FeedFault::Clock);
            }
            f64::NAN
        });
        (snapshot, now)
    }
    pub(crate) fn stop_viewing(&self) {
        self.reader
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .request_stop();
    }
}
impl Drop for BoundSource {
    fn drop(&mut self) {
        self.stop_viewing();
    }
}

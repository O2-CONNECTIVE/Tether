use super::provider::{Input, Provider};
use super::state::{FeedFault, Snapshot};
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc, Mutex,
};
use std::thread::{self, JoinHandle};
use std::time::Duration;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum WorkerEnd {
    SourceEnded,
    Canceled,
    Panicked,
}
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum JoinState {
    Pending,
    Joined(WorkerEnd),
}

pub(crate) struct ReaderOwner {
    cancel: Arc<AtomicBool>,
    snapshot: Arc<Mutex<Snapshot>>,
    worker: Option<JoinHandle<WorkerEnd>>,
    joined: Option<WorkerEnd>,
}

fn retained_fault(snapshot: &mut Snapshot, fault: FeedFault) {
    if snapshot.fault.is_none() || snapshot.fault == Some(FeedFault::Unavailable) {
        snapshot.fault = Some(fault);
    }
}
fn publish(shared: &Mutex<Snapshot>, mut snapshot: Snapshot, cancel: &AtomicBool) {
    // Check while holding the lock: a late read cannot undo request_stop's fault.
    let mut latest = shared.lock().unwrap_or_else(|e| e.into_inner());
    if cancel.load(Ordering::Acquire) {
        retained_fault(&mut snapshot, FeedFault::Stopped);
    }
    *latest = snapshot;
}

impl ReaderOwner {
    pub(crate) fn start<R, C>(mut provider: Provider<R, C>) -> std::io::Result<Self>
    where
        R: Input + Send + 'static,
        C: FnMut() -> Option<f64> + Send + 'static,
    {
        let snapshot = Arc::new(Mutex::new(provider.observation().0));
        let cancel = Arc::new(AtomicBool::new(false));
        let (shared, stop) = (snapshot.clone(), cancel.clone());
        let worker = thread::Builder::new()
            .name("mac-status-reader".into())
            .spawn(move || {
                let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| loop {
                    if stop.load(Ordering::Acquire) {
                        provider.stop();
                        publish(&shared, provider.observation().0, &stop);
                        return WorkerEnd::Canceled;
                    }
                    provider.pump_once();
                    publish(&shared, provider.observation().0, &stop);
                    if provider.ended() {
                        return WorkerEnd::SourceEnded;
                    }
                    thread::park_timeout(Duration::from_millis(20));
                }));
                match result {
                    Ok(end) => end,
                    Err(_) => {
                        let mut latest = shared.lock().unwrap_or_else(|e| e.into_inner());
                        retained_fault(&mut latest, FeedFault::Read);
                        WorkerEnd::Panicked
                    }
                }
            })?;
        Ok(Self {
            cancel,
            snapshot,
            worker: Some(worker),
            joined: None,
        })
    }

    // A retained snapshot is not a current observation. The future presentation
    // adapter must sample the native continuous clock and apply Snapshot::fresh.
    pub(crate) fn snapshot(&self) -> Snapshot {
        self.snapshot
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .clone()
    }

    pub(crate) fn request_stop(&self) {
        self.cancel.store(true, Ordering::Release);
        let mut latest = self.snapshot.lock().unwrap_or_else(|e| e.into_inner());
        retained_fault(&mut latest, FeedFault::Stopped);
        if let Some(worker) = &self.worker {
            worker.thread().unpark();
        }
    }

    // Pending retains the handle and cannot prove reader exit or host cleanup.
    pub(crate) fn poll_join(&mut self) -> JoinState {
        if let Some(end) = self.joined {
            return JoinState::Joined(end);
        }
        if !self.worker.as_ref().is_some_and(JoinHandle::is_finished) {
            return JoinState::Pending;
        }
        let end = self
            .worker
            .take()
            .unwrap()
            .join()
            .unwrap_or(WorkerEnd::Panicked);
        self.joined = Some(end);
        JoinState::Joined(end)
    }
}

impl Drop for ReaderOwner {
    fn drop(&mut self) {
        // Never silently detach an owned reader. Normal GUI lifecycle MUST call
        // request_stop and poll_join off the UI close path before dropping.
        // This last-resort synchronous join has no scheduling-time guarantee.
        self.request_stop();
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

#[cfg(test)]
#[path = "reader_owner_tests.rs"]
mod tests;

//! Fixed synchronization only, never a production launcher or cleanup marker.
use super::*;
use std::sync::atomic::{AtomicBool, Ordering};
pub(crate) struct Finish {
    ready: AtomicBool,
    release: AtomicBool,
    registered: AtomicBool,
    panic: bool,
}
impl Finish {
    pub(crate) fn ready(&self) -> bool {
        self.ready.load(Ordering::Acquire)
    }
    pub(crate) fn release(&self) {
        self.release.store(true, Ordering::Release);
    }
    pub(super) fn register(&self) {
        self.registered.store(true, Ordering::Release);
    }
    pub(super) fn wait(&self, gate: &ExitGate) {
        while !self.registered.load(Ordering::Acquire) {
            thread::sleep(Duration::from_millis(1));
        }
        assert!(!gate.join_monitor(), "native monitor cannot join itself");
        self.ready.store(true, Ordering::Release);
        while !self.release.load(Ordering::Acquire) {
            thread::sleep(Duration::from_millis(1));
        }
        assert!(
            !self.panic,
            "fixed outer panic after original IO/Wait joined"
        );
    }
}
impl NativeOuterOwner {
    pub(crate) fn fixture_held(
        producer: NativeIoSupervisor,
        panic: bool,
    ) -> std::io::Result<(Self, Arc<Finish>)> {
        let finish = Arc::new(Finish {
            ready: AtomicBool::new(false),
            release: AtomicBool::new(false),
            registered: AtomicBool::new(false),
            panic,
        });
        let owner = Self::start_inner(producer, super::Finish::Held(finish.clone()))?;
        Ok((owner, finish))
    }
    pub(crate) fn fixture_contended_join_refuses(&self) -> bool {
        let _lock = self
            .gate
            .0
            .monitor
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let gate = self.gate.clone();
        thread::spawn(move || !gate.join_monitor()).join().unwrap()
    }
    pub(crate) fn fixture_failed_join(&self) -> bool {
        match self.gate.0.monitor.try_lock() {
            Ok(m) => m.thread.is_none() && !m.joined,
            _ => false,
        }
    }
}

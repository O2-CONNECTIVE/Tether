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
    pub(crate) fn fixture_observe_join(producer: &mut NativeIoSupervisor) -> bool {
        super::observe_join(producer).is_some()
    }
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

// One-shot and calling-thread-local: cannot fail the native IO worker or an
// unrelated test's monitor. The exact captured closure is dropped as on Err.
thread_local! {
    static SPAWN_FAILURE: std::cell::RefCell<Option<std::sync::mpsc::SyncSender<()>>> =
        const { std::cell::RefCell::new(None) };
    static SPAWN_FAILED: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}
pub(super) fn spawn_monitor<F>(
    builder: thread::Builder,
    run: F,
) -> std::io::Result<thread::JoinHandle<()>>
where
    F: FnOnce() + Send + 'static,
{
    if SPAWN_FAILURE.with(|slot| slot.borrow().is_some()) {
        drop(run); // Closes/joins the original captured supervisor first.
        SPAWN_FAILED.with(|failed| failed.set(true));
        return Err(std::io::Error::other("fixed monitor spawn failure"));
    }
    builder.spawn(run)
}
impl NativeOuterOwner {
    pub(crate) fn fixture_fail_next_spawn(observer: std::sync::mpsc::SyncSender<()>) {
        SPAWN_FAILURE.with(|slot| assert!(slot.replace(Some(observer)).is_none()));
    }
    pub(crate) fn fixture_observe_failure_containment() {
        if SPAWN_FAILED.with(|failed| failed.replace(false)) {
            SPAWN_FAILURE.with(|slot| {
                slot.borrow_mut().take().unwrap().send(()).unwrap();
            });
        }
    }
}

//! Outer lifetime gate: physical reap alone never authorizes product teardown.
use crate::resident_port::duplex::owner::{CloseHandle, End, Supervisor};
use std::sync::{Arc, Mutex, TryLockError};
use std::thread::{self, JoinHandle};
use std::time::Duration;
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Outcome {
    Confirmed,
    NotSpawned,
    StartupRejected,
    Unknown,
}
fn safe(outcome: Option<Outcome>) -> bool {
    matches!(
        outcome,
        Some(Outcome::Confirmed | Outcome::NotSpawned | Outcome::StartupRejected)
    )
}
#[derive(Default)]
struct State {
    outcome: Option<Outcome>,
    requested: bool,
    wake: Option<Arc<dyn Fn() + Send + Sync>>,
}
#[derive(Default)]
struct Monitor {
    thread: Option<JoinHandle<()>>,
    joined: bool,
}
struct Inner {
    state: Mutex<State>,
    monitor: Mutex<Monitor>,
    close: CloseHandle,
}
#[derive(Clone)]
pub(crate) struct ExitGate(Arc<Inner>);
impl ExitGate {
    fn new(close: CloseHandle) -> Self {
        Self(Arc::new(Inner {
            state: Mutex::new(State::default()),
            monitor: Mutex::new(Monitor::default()),
            close,
        }))
    }
    pub(crate) fn request_exit(&self) -> bool {
        self.0.close.request(); // seals submissions before any exit decision
        self.0
            .state
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .requested = true;
        self.finish_requested()
    }
    pub(crate) fn finish_requested(&self) -> bool {
        let ready = {
            let s = self.0.state.lock().unwrap_or_else(|e| e.into_inner());
            s.requested && safe(s.outcome)
        };
        ready && self.join_monitor() && safe(self.outcome())
    }
    pub(crate) fn outcome(&self) -> Option<Outcome> {
        self.0
            .state
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .outcome
    }
    pub(crate) fn set_wake(&self, wake: Arc<dyn Fn() + Send + Sync>) {
        let ready = {
            let mut s = self.0.state.lock().unwrap_or_else(|e| e.into_inner());
            s.wake = Some(wake.clone());
            s.requested && safe(s.outcome)
        };
        if ready {
            wake();
        }
    }
    fn complete(&self, outcome: Outcome) {
        let wake = {
            let mut s = self.0.state.lock().unwrap_or_else(|e| e.into_inner());
            s.outcome = Some(outcome);
            if s.requested && safe(Some(outcome)) {
                s.wake.clone()
            } else {
                None
            }
        };
        if let Some(wake) = wake {
            wake();
        }
    }
    fn join_monitor(&self) -> bool {
        // Serialize join including completion, so another exit caller cannot
        // mistake a taken-but-still-joining handle for an already joined one.
        let mut monitor = match self.0.monitor.try_lock() {
            Ok(monitor) => monitor,
            // A monitor callback must not wait behind the caller joining it.
            // Neither a contending caller nor a taken handle is join proof.
            Err(TryLockError::WouldBlock) => return false,
            Err(TryLockError::Poisoned(error)) => error.into_inner(),
        };
        if monitor
            .thread
            .as_ref()
            .is_some_and(|h| h.thread().id() == thread::current().id())
        {
            return false;
        }
        if let Some(handle) = monitor.thread.take() {
            monitor.joined = handle.join().is_ok();
            if !monitor.joined {
                self.complete(Outcome::Unknown);
            }
        }
        monitor.joined
    }
}
pub(crate) struct StandbyOwner {
    pub(crate) gate: ExitGate,
}
impl StandbyOwner {
    pub(crate) fn start(mut producer: Supervisor) -> std::io::Result<Self> {
        let gate = ExitGate::new(producer.close_handle());
        let shared = gate.clone();
        let monitor = thread::Builder::new()
            .name("resident-gui-lifetime".into())
            .spawn(move || loop {
                if let Some(result) = producer.poll_join() {
                    let outcome = match result {
                        Ok(End::NotSpawned) => Outcome::NotSpawned,
                        Ok(End::Reaped(_)) if producer.startup_rejected() => {
                            Outcome::StartupRejected
                        }
                        Ok(End::Reaped(_)) if producer.cleanup_confirmed() => Outcome::Confirmed,
                        _ => Outcome::Unknown,
                    };
                    shared.complete(outcome);
                    return;
                }
                thread::sleep(Duration::from_millis(10));
            })?; // Failed spawn drops production Supervisor through its proof guard.
        gate.0
            .monitor
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .thread = Some(monitor);
        Ok(Self { gate })
    }
    // Setup failure or unwind must join this same owner before returning.
    pub(crate) fn guarded(
        &mut self,
        body: impl FnOnce() -> Result<(), &'static str>,
    ) -> Result<(), &'static str> {
        let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(body));
        self.join();
        result.unwrap_or(Err("RESIDENT_GUI_UNAVAILABLE"))
    }
    pub(crate) fn join(&mut self) {
        self.gate.0.close.request();
        while !(self.gate.join_monitor() && safe(self.gate.outcome())) {
            thread::park_timeout(Duration::from_secs(1));
        }
    }
}
impl Drop for StandbyOwner {
    fn drop(&mut self) {
        self.join();
    }
}

#[cfg(test)]
#[path = "standby_lifecycle_tests.rs"]
mod tests;

#[cfg(resident_native_source)]
#[path = "native_outer_lifecycle.rs"]
pub(crate) mod native;

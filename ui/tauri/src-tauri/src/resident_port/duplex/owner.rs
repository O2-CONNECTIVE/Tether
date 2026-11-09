//! Parent-owned supervision. Production Drop also requires actual Close proof.
use super::super::model::{IntentRequest, Receipt};
use super::super::observation::Observation;
use super::exchange::Shared;
use super::pump::Pump;
use crate::mac_status::ContinuousClock;
use std::io;
use std::process::{Child, ExitStatus};
use std::sync::{mpsc::Receiver, Arc, Mutex};
use std::thread::{self, JoinHandle};
use std::time::Duration;

pub(crate) struct BoundDuplex {
    shared: Arc<Mutex<Shared>>,
    clock: ContinuousClock,
}
impl BoundDuplex {
    pub(crate) fn current(&self) -> Option<Observation> {
        let mut shared = self.shared.lock().unwrap_or_else(|e| e.into_inner());
        match self.clock.now_millis() {
            Some(now) => shared.current(now),
            None => {
                shared.fault();
                None
            }
        }
    }
    pub(crate) fn request(&self, request: IntentRequest) -> io::Result<Receiver<Receipt>> {
        let mut shared = self.shared.lock().unwrap_or_else(|e| e.into_inner());
        let Some(now) = self.clock.now_millis() else {
            shared.fault();
            return Err(unavailable());
        };
        Ok(shared.submit(request, now))
    }
    // Native bootstrap must finish this off the UI thread BEFORE installing the
    // port/creating WebView. Timeout returns failure, never a fabricated status.
    // The separately retained supervisor continues owning this exact child.
    pub(crate) fn first(&self) -> io::Result<Observation> {
        self.first_with_timeout(Duration::from_secs(15))
    }
    pub(crate) fn first_with_timeout(&self, timeout: Duration) -> io::Result<Observation> {
        let start = self.clock.now_millis().ok_or_else(unavailable)?;
        loop {
            if let Some(status) = self.current() {
                if status.valid_for_ms > 0 {
                    return Ok(status);
                }
            }
            if self
                .clock
                .now_millis()
                .is_none_or(|now| now < start || u128::from(now - start) >= timeout.as_millis())
            {
                #[cfg(test)]
                self.trace_first("deadline-or-clock", start);
                CloseHandle(self.shared.clone()).request();
                return Err(unavailable());
            }
            let rejected = self
                .shared
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .startup
                .received();
            if rejected {
                #[cfg(test)]
                self.trace_first("startup-rejected", start);
                return Err(unavailable());
            }
            let failed = self
                .shared
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .tracking
                .failed;
            if failed {
                #[cfg(test)]
                self.trace_first("tracking-fault", start);
                return Err(unavailable());
            }
            thread::sleep(Duration::from_millis(5));
        }
    }
    #[cfg(test)]
    fn trace_first(&self, reason: &str, start: u64) {
        let state = self.shared.lock().unwrap_or_else(|e| e.into_inner());
        eprintln!("RESIDENT_FIXTURE_FIRST reason={reason} shared={:p} start={start} now={:?} source={:?} failed={} read={:?}",
            Arc::as_ptr(&self.shared), self.clock.now_millis(), state.tracking.source_time(), state.tracking.failed, state.fixture_trace);
    }
}
// No Drop: losing/hiding a viewer must NOT close the process control pipe.
#[derive(Debug)]
pub(crate) enum End {
    NotSpawned,
    Reaped(ExitStatus),
}
pub(crate) struct Supervisor {
    shared: Arc<Mutex<Shared>>,
    worker: Option<JoinHandle<io::Result<End>>>,
    known: bool,
    cleanup_confirmed: bool,
    startup_rejected: bool,
    not_spawned: bool,
    require_cleanup: bool,
}
impl Supervisor {
    // Explicit application Quit/full transport loss only, never viewer close.
    // The reviewed Go bridge closes exactly once on EOF and retains unknown
    // original cleanup. This hook is not an OFF receipt or cleanup proof.
    pub(crate) fn close_handle(&self) -> CloseHandle {
        CloseHandle(self.shared.clone())
    }
    pub(crate) fn request_close_transport(&self) {
        self.close_handle().request();
    }
    // Private proof only after exact Child AND this IO/supervisor joined.
    // Reaped/known alone must never authorize confirmed application teardown.
    pub(crate) fn startup_rejected(&self) -> bool {
        self.known && self.startup_rejected
    }
    pub(crate) fn cleanup_confirmed(&self) -> bool {
        self.known && self.cleanup_confirmed
    }
    pub(crate) fn poll_join(&mut self) -> Option<io::Result<End>> {
        if !self.worker.as_ref()?.is_finished() {
            return None;
        }
        let outcome = self
            .worker
            .take()
            .unwrap()
            .join()
            .unwrap_or_else(|_| Err(unavailable()));
        self.record(&outcome);
        Some(outcome)
    }
}
fn drive(
    mut pump: Pump,
    clock: ContinuousClock,
    shared: Arc<Mutex<Shared>>,
    child: &mut Child,
) -> ExitStatus {
    drive_io(|state, clock| pump.step(state, clock), clock, shared);
    // Full transport loss closes this exact Child's owned IO before its Wait.
    drop(pump);
    reap(child)
}
// Shared control flow, not an owner/Wait adapter. The closure only borrows the
// caller's concrete Pump. Returning here never establishes process completion.
pub(super) fn drive_io(
    mut step: impl FnMut(&mut Shared, &ContinuousClock) -> Result<bool, ()>,
    clock: ContinuousClock,
    shared: Arc<Mutex<Shared>>,
) {
    struct FaultOnDrop {
        shared: Arc<Mutex<Shared>>,
        armed: bool,
    }
    impl Drop for FaultOnDrop {
        fn drop(&mut self) {
            if self.armed {
                self.shared
                    .lock()
                    .unwrap_or_else(|e| e.into_inner())
                    .fault();
            }
        }
    }
    let mut fault = FaultOnDrop {
        shared: shared.clone(),
        armed: true,
    };
    loop {
        let outcome = {
            let mut state = shared.lock().unwrap_or_else(|e| e.into_inner());
            let result = step(&mut state, &clock);
            if result.is_err() {
                state.fault();
            }
            result
        };
        if !matches!(outcome, Ok(false)) {
            break;
        }
        // Do not stop on child exit: stdout must drain through actual EOF.
        thread::sleep(Duration::from_millis(5));
    }
    fault.armed = false;
}
fn reap(child: &mut Child) -> ExitStatus {
    loop {
        match child.wait() {
            Ok(status) => return status,
            Err(_) => thread::sleep(Duration::from_millis(100)),
        }
    }
}
fn unavailable() -> io::Error {
    io::Error::other("RESIDENT_SOURCE_UNAVAILABLE")
}
impl Drop for Supervisor {
    fn drop(&mut self) {
        if self.require_cleanup && self.worker.is_some() {
            self.request_close_transport();
        }
        if let Some(worker) = self.worker.take() {
            let result = worker.join().unwrap_or_else(|_| Err(unavailable()));
            self.record(&result);
        }
        if !self.can_release() {
            // Unknown cleanup is not recovered. Retain the parent, never let
            // setup error/unwind/failed monitor creation abandon this boundary.
            loop {
                thread::park_timeout(Duration::from_secs(1));
            }
        }
    }
}
#[derive(Clone)]
pub(crate) struct CloseHandle(Arc<Mutex<Shared>>);
impl CloseHandle {
    pub(crate) fn request(&self) {
        let mut shared = self.0.lock().unwrap_or_else(|e| e.into_inner());
        if shared.closure.requested() {
            return;
        }
        match ContinuousClock::new().and_then(|clock| clock.now_millis()) {
            Some(now) => shared.close_control(now),
            None => shared.fault(),
        }
    }
}
#[cfg(test)]
#[path = "fixture_launcher.rs"]
pub(crate) mod fixture_launcher;
#[cfg(test)]
#[path = "owner_guard_tests.rs"]
mod guard_tests;
#[path = "internal_start.rs"]
mod launcher;
#[path = "owner_proof.rs"]
mod proof;

#[cfg(resident_native_source)]
#[path = "native_supervisor.rs"]
pub(crate) mod native;

// Explicit disposable-CI composition, never a normal startup path.
#[cfg(all(target_os = "macos", resident_gui_fixture))]
impl Supervisor {
    pub(crate) fn gui_fixture(
        command: std::process::Command,
    ) -> io::Result<(Self, io::Result<BoundDuplex>)> {
        Self::start(command, true, launcher::Attachment::Normal)
    }
}

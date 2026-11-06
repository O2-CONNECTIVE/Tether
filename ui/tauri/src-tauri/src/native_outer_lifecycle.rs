//! Actual native outer monitor join; UNKNOWN never permits application Quit.
use super::*;
use crate::resident_port::duplex::owner::native::{NativeIoSupervisor, NativeJoined};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct Observed {
    native: NativeJoined,
    wire: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct NativeOuterJoined {
    observed: Observed,
}
impl NativeOuterJoined {
    pub(crate) fn native(self) -> NativeJoined {
        self.observed.native
    }
    pub(crate) fn wire_close_confirmed(self) -> bool {
        self.observed.wire
    }
}
pub(crate) struct NativeOuterOwner {
    gate: ExitGate,
    observed: Arc<Mutex<Option<Observed>>>,
    #[cfg(resident_launch_fixture)]
    finish: Option<Arc<fixture::Finish>>,
}
impl NativeOuterOwner {
    pub(crate) fn start(producer: NativeIoSupervisor) -> std::io::Result<Self> {
        Self::start_inner(producer, Finish::Normal)
    }
    fn start_inner(mut producer: NativeIoSupervisor, finish: Finish) -> std::io::Result<Self> {
        let gate = ExitGate::new(producer.close_handle());
        let state = gate.clone();
        let observed = Arc::new(Mutex::new(None));
        let output = observed.clone();
        #[cfg(resident_launch_fixture)]
        let fixture_finish = finish.fixture();
        let monitor = thread::Builder::new()
            .name("resident-native-outer-owner".into())
            .spawn(move || {
                loop {
                    if let Some(observed) = observe_join(&mut producer) {
                        *output.lock().unwrap_or_else(|e| e.into_inner()) = Some(observed);
                        // Even Exit(0) + EOF/close + IO join cannot release the
                        // retained UNKNOWN native record or authorize full Quit.
                        state.complete(Outcome::Unknown);
                        finish.wait(&state);
                        return;
                    }
                    thread::sleep(Duration::from_millis(10));
                }
            })?; // On failed spawn, the captured native supervisor closes/joins.
        gate.0
            .monitor
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .thread = Some(monitor);
        #[cfg(resident_launch_fixture)]
        if let Some(finish) = &fixture_finish {
            finish.register();
        }
        Ok(Self {
            gate,
            observed,
            #[cfg(resident_launch_fixture)]
            finish: fixture_finish,
        })
    }
    pub(crate) fn request_exit(&self) -> bool {
        self.gate.request_exit()
    }
    pub(crate) fn finish_requested(&self) -> bool {
        self.gate.finish_requested()
    }
    pub(crate) fn outcome(&self) -> Option<Outcome> {
        self.gate.outcome()
    }
    pub(crate) fn poll_join(&self) -> Option<NativeOuterJoined> {
        let ready = match self.gate.0.monitor.try_lock() {
            Ok(m) => m.joined || m.thread.as_ref().is_some_and(|h| h.is_finished()),
            Err(TryLockError::WouldBlock) => false,
            Err(TryLockError::Poisoned(e)) => {
                let m = e.into_inner();
                m.joined || m.thread.as_ref().is_some_and(|h| h.is_finished())
            }
        };
        if !ready || !self.gate.join_monitor() {
            return None;
        }
        // Neither published IO evidence nor a finished/taken monitor handle is
        // this proof: construct it only after the existing actual join guard.
        self.observed
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .map(|observed| NativeOuterJoined { observed })
    }
}
impl Drop for NativeOuterOwner {
    fn drop(&mut self) {
        #[cfg(resident_launch_fixture)]
        if let Some(finish) = &self.finish {
            finish.release();
        }
        self.gate.0.close.request();
        while !self.gate.join_monitor() {
            thread::park_timeout(Duration::from_secs(1));
        }
        // Component monitor ownership only. ExitGate remains UNKNOWN/false;
        // the original native record is retained, with no full cleanup permit.
    }
}
fn observe_join(producer: &mut NativeIoSupervisor) -> Option<Observed> {
    producer
        .joined()
        .or_else(|| producer.poll_join())
        .map(|native| Observed {
            native,
            wire: producer.wire_close_confirmed(),
        })
}
enum Finish {
    Normal,
    #[cfg(resident_launch_fixture)]
    Held(Arc<fixture::Finish>),
}
impl Finish {
    fn wait(self, _gate: &ExitGate) {
        #[cfg(resident_launch_fixture)]
        if let Self::Held(finish) = self {
            finish.wait(_gate);
        }
    }
    #[cfg(resident_launch_fixture)]
    fn fixture(&self) -> Option<Arc<fixture::Finish>> {
        match self {
            Self::Normal => None,
            Self::Held(f) => Some(f.clone()),
        }
    }
}
#[cfg(resident_launch_fixture)]
#[path = "native_outer_fixture.rs"]
mod fixture;
#[cfg(resident_launch_fixture)]
pub(crate) type FixtureFinish = fixture::Finish;

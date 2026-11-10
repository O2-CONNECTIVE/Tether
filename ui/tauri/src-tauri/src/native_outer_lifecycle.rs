//! Native terminal proof plus actual worker and monitor joins gate application Quit.
use super::*;
use crate::resident_port::duplex::owner::native::{
    NativeIoSupervisor, NativeJoined, NativeOutcome,
};

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
        let builder = thread::Builder::new().name("resident-native-outer-owner".into());
        let run = move || loop {
            if let Some(observed) = observe_join(&mut producer) {
                *output.lock().unwrap_or_else(|e| e.into_inner()) = Some(observed);
                // Publish no success before this very monitor actually joins.
                // A later panic must leave the native evidence insufficient.
                state.complete(Outcome::Unknown);
                finish.wait(&state);
                return;
            }
            thread::sleep(Duration::from_millis(10));
        };
        #[cfg(resident_launch_fixture)]
        let monitor = fixture::spawn_monitor(builder, run)?;
        #[cfg(not(resident_launch_fixture))]
        let monitor = builder.spawn(run)?;
        // On failed spawn, the captured native supervisor closes/joins.
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
        let _ = self.poll_join();
        self.gate.request_exit()
    }
    pub(crate) fn finish_requested(&self) -> bool {
        let _ = self.poll_join();
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
        let observed = *self.observed.lock().unwrap_or_else(|e| e.into_inner());
        observed.map(|observed| {
            // Only this successful actual outer join can publish a safe outcome.
            self.gate.complete(classify(observed));
            NativeOuterJoined { observed }
        })
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
        // Drop alone does not mint a success outcome. Terminal native records
        // remain tombstoned; unknown original records are never replaced.
    }
}
fn classify(observed: Observed) -> Outcome {
    use crate::native_preparation::{ExitObservation, State};
    match observed.native.outcome() {
        NativeOutcome::UnavailableBeforeOwnership => Outcome::NotSpawned,
        NativeOutcome::Prepared(State::RetiredUnlaunched) => Outcome::NotSpawned,
        NativeOutcome::Active {
            exit: ExitObservation::Exit(0),
            io: State::ActiveIoRetired,
            unwound: false,
        } if observed.native.terminal_confirmed() && observed.wire => Outcome::Confirmed,
        NativeOutcome::Active {
            exit: ExitObservation::Exit(2),
            io: State::ActiveIoRetired,
            unwound: false,
        } if observed.native.terminal_confirmed() && observed.native.startup_rejected() => {
            Outcome::StartupRejected
        }
        // Refused is ambiguous (including an already retained native record),
        // not a positively proven NotSpawned. Never promote exit status alone.
        _ => Outcome::Unknown,
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

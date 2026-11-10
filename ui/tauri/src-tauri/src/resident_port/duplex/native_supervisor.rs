//! Concrete native IO worker only; no full cleanup or application Quit permit.
use super::*;
use crate::native_preparation::{
    ExitObservation, NativePreparation, NativeTerminal, Preparation, StartupPreparation, State,
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum NativeOutcome {
    Refused,
    UnavailableBeforeOwnership,
    Prepared(State),
    Active {
        exit: ExitObservation,
        io: State,
        unwound: bool,
    },
    Unknown,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct NativeJoined {
    outcome: NativeOutcome,
    terminal: bool,
    startup_rejected: bool,
}
// Only the worker can produce the affine native component; only actual join
// below can turn it into durable read-only completion evidence.
struct NativeWorkerEnd {
    outcome: NativeOutcome,
    terminal: Option<NativeTerminal>,
    startup_rejected: bool,
}
impl NativeWorkerEnd {
    fn incomplete(outcome: NativeOutcome) -> Self {
        Self {
            outcome,
            terminal: None,
            startup_rejected: false,
        }
    }
}
impl NativeJoined {
    pub(crate) fn terminal_confirmed(self) -> bool {
        self.terminal
    }
    pub(crate) fn startup_rejected(self) -> bool {
        self.startup_rejected
    }
    pub(crate) fn outcome(self) -> NativeOutcome {
        self.outcome
    }
}

pub(crate) struct NativeIoSupervisor {
    shared: Arc<Mutex<Shared>>,
    worker: Option<JoinHandle<NativeWorkerEnd>>,
    joined: Option<NativeJoined>,
}
enum Bootstrap {
    Preparation,
    #[cfg(resident_launch_fixture)]
    PreparedFixture,
    #[cfg(resident_launch_fixture)]
    Fixture(crate::native_preparation::FixedActiveFixture, bool),
}
impl Bootstrap {
    fn open(self) -> (StartupPreparation, bool, bool) {
        match self {
            Self::Preparation => {
                let preparation = match NativePreparation::startup() {
                    StartupPreparation::Prepared(Preparation::Owned(mut owner)) => {
                        let _ = owner.launch();
                        StartupPreparation::Prepared(Preparation::Owned(owner))
                    }
                    other => other,
                };
                (preparation, false, false)
            }
            #[cfg(resident_launch_fixture)]
            Self::PreparedFixture => (
                StartupPreparation::Prepared(NativePreparation::fixture(
                    crate::native_preparation::FixedFixture::Normal,
                )),
                false,
                false,
            ),
            #[cfg(resident_launch_fixture)]
            Self::Fixture(mode, unwind) => {
                let coalesced = matches!(
                    mode,
                    crate::native_preparation::FixedActiveFixture::FiniteCoalesced
                );
                (
                    StartupPreparation::Prepared(NativePreparation::fixture_active(mode)),
                    unwind,
                    coalesced,
                )
            }
        }
    }
}
impl NativeIoSupervisor {
    // Closed launch composition; unavailable only before original ownership.
    pub(crate) fn prepare() -> io::Result<(Self, io::Result<BoundDuplex>)> {
        Self::start(Bootstrap::Preparation)
    }
    #[cfg(resident_launch_fixture)]
    pub(crate) fn fixture(
        mode: crate::native_preparation::FixedActiveFixture,
        unwind: bool,
    ) -> io::Result<(Self, io::Result<BoundDuplex>)> {
        Self::start(Bootstrap::Fixture(mode, unwind))
    }
    #[cfg(resident_launch_fixture)]
    pub(crate) fn fixture_prepared() -> io::Result<(Self, io::Result<BoundDuplex>)> {
        Self::start(Bootstrap::PreparedFixture)
    }
    fn start(bootstrap: Bootstrap) -> io::Result<(Self, io::Result<BoundDuplex>)> {
        let shared = Arc::new(Mutex::new(Shared::default()));
        let state = shared.clone();
        let (send, receive) = std::sync::mpsc::sync_channel(1);
        let worker = thread::Builder::new()
            .name("resident-native-io-owner".into())
            .spawn(move || run(bootstrap, state, send))?;
        // Install the parent before waiting for attachment, including unwind.
        let supervisor = Self {
            shared,
            worker: Some(worker),
            joined: None,
        };
        let source = receive.recv().unwrap_or_else(|_| Err(unavailable()));
        Ok((supervisor, source))
    }
    pub(crate) fn close_handle(&self) -> CloseHandle {
        CloseHandle(self.shared.clone())
    }
    pub(crate) fn request_close_transport(&self) {
        self.close_handle().request();
    }
    pub(crate) fn joined(&self) -> Option<NativeJoined> {
        self.joined
    }
    pub(crate) fn poll_join(&mut self) -> Option<NativeJoined> {
        if !self.worker.as_ref()?.is_finished() {
            return None;
        }
        Some(self.join_worker())
    }
    fn join_worker(&mut self) -> NativeJoined {
        let outcome = self
            .worker
            .take()
            .unwrap()
            .join()
            .unwrap_or_else(|_| NativeWorkerEnd::incomplete(NativeOutcome::Unknown));
        let joined = NativeJoined {
            outcome: outcome.outcome,
            terminal: outcome.terminal.is_some(),
            startup_rejected: outcome.startup_rejected,
        }; // Only after the actual join, including TLS destructor completion.
        self.joined = Some(joined);
        joined
    }
    // Wire component only, never cleanup_confirmed / outer joins / Quit.
    pub(crate) fn wire_close_confirmed(&self) -> bool {
        self.joined.is_some()
            && self
                .shared
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .closure
                .confirmed()
    }
}
impl Drop for NativeIoSupervisor {
    fn drop(&mut self) {
        if self.worker.is_some() {
            self.request_close_transport();
            let _ = self.join_worker(); // Never detach a pending original worker.
        }
        // Terminal records remain tombstoned; uncertain original records remain UNKNOWN.
    }
}
fn run(
    bootstrap: Bootstrap,
    shared: Arc<Mutex<Shared>>,
    send: std::sync::mpsc::SyncSender<io::Result<BoundDuplex>>,
) -> NativeWorkerEnd {
    let (preparation, _unwind, _coalesced) = bootstrap.open();
    let preparation = match preparation {
        StartupPreparation::UnavailableBeforeOwnership => {
            let _ = send.try_send(Err(unavailable()));
            return NativeWorkerEnd::incomplete(NativeOutcome::UnavailableBeforeOwnership);
        }
        StartupPreparation::Prepared(preparation) => preparation,
    };
    let mut original = match preparation {
        Preparation::Refused => {
            let _ = send.try_send(Err(unavailable()));
            return NativeWorkerEnd::incomplete(NativeOutcome::Refused);
        }
        Preparation::Owned(owner) | Preparation::Unknown(owner) => owner,
    };
    if original.state() == State::OwnedUnlaunched {
        let _ = send.try_send(Err(unavailable()));
        return NativeWorkerEnd::incomplete(NativeOutcome::Prepared(original.retire()));
    }
    // Inactive unknown preparation has no Process to Wait. Active uncertainty
    // instead retains the same affine guard through exact completion below.
    if original.poll_exit() == ExitObservation::Unavailable {
        let _ = send.try_send(Err(unavailable()));
        return NativeWorkerEnd::incomplete(NativeOutcome::Prepared(State::Unknown));
    }
    let mut clean_eof = false;
    let execution = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let mut pump = Pump::take_active(&mut original).map_err(|_| unavailable())?;
        let clock = ContinuousClock::new().ok_or_else(unavailable)?;
        let view_clock = ContinuousClock::new().ok_or_else(unavailable)?;
        if send
            .try_send(Ok(BoundDuplex {
                shared: shared.clone(),
                clock: view_clock,
            }))
            .is_err()
        {
            CloseHandle(shared.clone()).request();
        }
        drive_io(
            |state, clock| {
                #[cfg(resident_launch_fixture)]
                if _unwind {
                    let _ = pump.step(state, clock);
                    panic!("fixed native supervisor IO unwind");
                }
                let result = pump.step(state, clock);
                clean_eof = matches!(result, Ok(true));
                result
            },
            clock,
            shared.clone(),
        );
        Ok::<_, io::Error>(())
    }));
    if !matches!(execution, Ok(Ok(()))) {
        shared.lock().unwrap_or_else(|e| e.into_inner()).fault();
    }
    let _ = send.try_send(Err(unavailable()));
    // Full transport loss closes only this owner's control. UNKNOWN prevents
    // ordinary IO but still permits a close request and same-Process polling.
    if clean_eof && original.state() == State::Active {
        let _ = original.io().half_close_control();
    }
    if !clean_eof || original.state() == State::Unknown {
        original.request_active_close();
    }
    let mut drained = clean_eof;
    let exit = loop {
        let mut progressed = false;
        if !drained {
            let discard = original.discard_observation();
            drained = discard.is_eof();
            progressed = discard == crate::native_preparation::DiscardObservation::Progress;
        }
        let value = original.poll_exit();
        if drained && matches!(value, ExitObservation::Exit(_) | ExitObservation::Signal(_)) {
            break value;
        }
        // Every bounded chunk polls the same Process, then offers a scheduling hint.
        // A timer after successful reads needlessly throttles finite drainage.
        if progressed {
            thread::yield_now();
            continue;
        }
        // No replacement / signal / detached TLS exit on pending or unknown.
        // Fixed fixture models a delayed timer delivery, not a timeout success.
        #[cfg(resident_launch_fixture)]
        if _coalesced {
            thread::sleep(Duration::from_millis(25));
            continue;
        }
        thread::sleep(Duration::from_millis(5));
    };
    let io = original.retire_active_io();
    // IO retirement alone cannot erase malformed/missing authoritative evidence.
    // The same worker reads the original Shared proof before consuming its owner.
    let (wire, startup) = {
        let state = shared.lock().unwrap_or_else(|e| e.into_inner());
        (state.closure.confirmed(), state.startup.confirmed())
    };
    let eligible = clean_eof
        && matches!(execution, Ok(Ok(())))
        && ((exit == ExitObservation::Exit(0) && wire)
            || (exit == ExitObservation::Exit(2) && startup));
    let terminal = if eligible {
        original.finish_active()
    } else {
        None
    };
    NativeWorkerEnd {
        outcome: NativeOutcome::Active {
            exit,
            io,
            unwound: execution.is_err(),
        },
        startup_rejected: terminal.is_some() && exit == ExitObservation::Exit(2) && startup,
        terminal,
    }
}

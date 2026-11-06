//! Concrete native IO worker only; no full cleanup or application Quit permit.
use super::*;
use crate::native_preparation::{ExitObservation, NativePreparation, Preparation, State};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum NativeOutcome {
    Refused,
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
}
impl NativeJoined {
    pub(crate) fn outcome(self) -> NativeOutcome {
        self.outcome
    }
}

pub(crate) struct NativeIoSupervisor {
    shared: Arc<Mutex<Shared>>,
    worker: Option<JoinHandle<NativeOutcome>>,
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
    fn open(self) -> (Preparation, bool) {
        match self {
            Self::Preparation => (NativePreparation::prepare(), false),
            #[cfg(resident_launch_fixture)]
            Self::PreparedFixture => (
                NativePreparation::fixture(crate::native_preparation::FixedFixture::Normal),
                false,
            ),
            #[cfg(resident_launch_fixture)]
            Self::Fixture(mode, unwind) => (NativePreparation::fixture_active(mode), unwind),
        }
    }
}
impl NativeIoSupervisor {
    // Concrete closed preparation only. No signed active launcher exists yet.
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
            .unwrap_or(NativeOutcome::Unknown);
        let joined = NativeJoined { outcome }; // Only after the actual join.
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
        // No release notification: native retains its original UNKNOWN record.
    }
}
fn run(
    bootstrap: Bootstrap,
    shared: Arc<Mutex<Shared>>,
    send: std::sync::mpsc::SyncSender<io::Result<BoundDuplex>>,
) -> NativeOutcome {
    let (preparation, _unwind) = bootstrap.open();
    let mut original = match preparation {
        Preparation::Refused => {
            let _ = send.try_send(Err(unavailable()));
            return NativeOutcome::Refused;
        }
        Preparation::Owned(owner) | Preparation::Unknown(owner) => owner,
    };
    if original.state() == State::OwnedUnlaunched {
        let _ = send.try_send(Err(unavailable()));
        return NativeOutcome::Prepared(original.retire());
    }
    // Inactive unknown preparation has no Process to Wait. Active uncertainty
    // instead retains the same affine guard through exact completion below.
    if original.poll_exit() == ExitObservation::Unavailable {
        let _ = send.try_send(Err(unavailable()));
        return NativeOutcome::Prepared(State::Unknown);
    }
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
                pump.step(state, clock)
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
    if original.state() == State::Active {
        let _ = original.io().half_close_control();
    }
    if original.state() == State::Unknown {
        original.request_active_close();
    }
    let exit = loop {
        let value = original.poll_exit();
        if matches!(value, ExitObservation::Exit(_) | ExitObservation::Signal(_)) {
            break value;
        }
        // No replacement / signal / detached TLS exit on pending or unknown.
        thread::sleep(Duration::from_millis(5));
    };
    let io = original.retire_active_io();
    NativeOutcome::Active {
        exit,
        io,
        unwound: execution.is_err(),
    }
}

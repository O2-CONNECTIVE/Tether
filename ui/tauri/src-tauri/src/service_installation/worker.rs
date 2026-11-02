use super::{retained::Retained, Calls, Fault, Observation, Seed};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

type Reply = (u64, Result<(), Fault>);
pub(super) struct Worker {
    requests: mpsc::SyncSender<u64>,
    replies: mpsc::Receiver<Reply>,
    stop: Arc<AtomicBool>,
    join: Option<JoinHandle<()>>,
    pending: Option<u64>,
    sequence: u64,
    terminal: Option<Observation>,
}
impl Worker {
    pub fn start(seed: Seed, calls: impl Calls) -> Result<Self, Fault> {
        let (requests, receive) = mpsc::sync_channel(1);
        let (send, replies) = mpsc::sync_channel(1);
        let stop = Arc::new(AtomicBool::new(false));
        let worker_stop = stop.clone();
        let join = thread::Builder::new()
            .name("installation-read".into())
            .spawn(move || {
                let mut owner = Retained::new(seed, calls);
                // Owner lives outside catch scope. Its OS handles have no Drop, and
                // the native read transfers pending allocations to raw ownership.
                // Thus unwind cannot destroy them before this fail-stop disposition.
                guarded(&mut owner, &worker_stop, receive, send);
            })
            .map_err(|_| Fault::Unknown)?;
        Ok(Self {
            requests,
            replies,
            stop,
            join: Some(join),
            pending: None,
            sequence: 0,
            terminal: None,
        })
    }
    pub fn observe(&mut self) -> Observation {
        if let Some(terminal) = self.terminal {
            return terminal;
        }
        if let Some(expected) = self.pending {
            match self.replies.try_recv() {
                Ok((sequence, result)) if sequence == expected => {
                    self.pending = None;
                    if let Err(fault) = result {
                        return self.fail(match fault {
                            Fault::Denied => Observation::Denied,
                            Fault::Unknown => Observation::Unknown,
                        });
                    }
                    return Observation::Checked(sequence);
                }
                Ok(_) | Err(mpsc::TryRecvError::Disconnected) => {
                    return self.fail(Observation::Unknown)
                }
                Err(mpsc::TryRecvError::Empty) => return Observation::Pending,
            }
        }
        let Some(sequence) = self.sequence.checked_add(1) else {
            return self.fail(Observation::Unknown);
        };
        match self.requests.try_send(sequence) {
            Ok(()) => {
                self.pending = Some(sequence);
                self.sequence = sequence;
                Observation::Pending
            }
            Err(_) => self.fail(Observation::Unknown),
        }
    }
    fn fail(&mut self, observation: Observation) -> Observation {
        self.stop.store(true, Ordering::Release);
        self.terminal = Some(observation);
        observation
    }
}
pub(super) fn guarded<C: Calls>(
    owner: &mut Retained<C>,
    stop: &AtomicBool,
    requests: mpsc::Receiver<u64>,
    replies: mpsc::SyncSender<Reply>,
) {
    let result = catch_unwind(AssertUnwindSafe(|| {
        run(owner, stop, requests, replies);
        owner.close()
    }));
    if !matches!(result, Ok(Ok(()))) {
        owner.fatal();
    }
}

fn run<C: Calls>(
    owner: &mut Retained<C>,
    stop: &AtomicBool,
    requests: mpsc::Receiver<u64>,
    replies: mpsc::SyncSender<Reply>,
) {
    let mut acquired = false;
    while !stop.load(Ordering::Acquire) {
        let sequence = match requests.recv_timeout(Duration::from_millis(10)) {
            Ok(sequence) => sequence,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => return,
        };
        let result = if acquired {
            owner.recheck(stop)
        } else {
            owner.acquire(stop)
        };
        acquired = result.is_ok();
        if replies.try_send((sequence, result)).is_err() || result.is_err() {
            return;
        }
    }
}
// INACTIVE candidate: this two-second policy can preempt configured cleanup.
// Product availability acceptance is unresolved; production gate stays closed.
impl Drop for Worker {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        let Some(join) = self.join.take() else {
            return;
        };
        let deadline = Instant::now() + Duration::from_secs(2);
        while !join.is_finished() && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(1));
        }
        // This is GUI-process fail-stop, NOT service shutdown or cleanup proof.
        // A synchronous OS query can fail to return; no graceful bound claimed.
        if !join.is_finished() {
            std::process::abort();
        }
        if join.join().is_err() {
            std::process::abort();
        }
    }
}

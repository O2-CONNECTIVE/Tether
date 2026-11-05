use super::{JoinState, ReaderOwner, WorkerEnd};
use crate::mac_status::provider::{Input, Provider, ReadStep};
use crate::mac_status::state::FeedFault;
use std::collections::VecDeque;
use std::sync::{
    atomic::{AtomicUsize, Ordering},
    mpsc, Arc,
};
use std::thread;
use std::time::{Duration, Instant};

enum Step {
    Data(Vec<u8>),
    Eof,
    Error,
    Panic,
    Gate(mpsc::Sender<()>, mpsc::Receiver<Vec<u8>>),
}
struct Fake {
    steps: VecDeque<Step>,
    drops: Arc<AtomicUsize>,
}
impl Drop for Fake {
    fn drop(&mut self) {
        self.drops.fetch_add(1, Ordering::SeqCst);
    }
}
impl Input for Fake {
    fn read_once(&mut self, out: &mut [u8; 4096]) -> Result<ReadStep, ()> {
        let data = match self.steps.pop_front() {
            Some(Step::Data(data)) => data,
            Some(Step::Gate(entered, release)) => {
                entered.send(()).unwrap();
                release.recv_timeout(Duration::from_secs(2)).unwrap()
            }
            Some(Step::Eof) => return Ok(ReadStep::Eof),
            Some(Step::Error) => return Err(()),
            Some(Step::Panic) => panic!("closed reader fixture"),
            None => return Ok(ReadStep::Pending),
        };
        out[..data.len()].copy_from_slice(&data);
        Ok(ReadStep::Bytes(data.len()))
    }
}
fn frame(seq: u64, terminal: bool) -> Vec<u8> {
    format!(r#"{{"version":1,"source_continuous_ms":100000,"seq":{seq},"phase":"{}","reason":"none","grant_remaining_ms":{},"starting_workers":0,"running_workers":0,"stopping_workers":0,"cleanup":"{}","terminal_cleanup_unknown":false,"terminal":{terminal}}}
"#, if terminal {"closed"} else {"connected"}, if terminal {0} else {2000}, if terminal {"confirmed"} else {"pending"}).into_bytes()
}
fn start(steps: Vec<Step>, drops: Arc<AtomicUsize>) -> ReaderOwner {
    ReaderOwner::start(
        Provider::new(
            Fake {
                steps: steps.into(),
                drops,
            },
            || Some(100.0),
        )
        .unwrap(),
    )
    .unwrap()
}
fn join(owner: &mut ReaderOwner) -> WorkerEnd {
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        match owner.poll_join() {
            JoinState::Joined(end) => return end,
            JoinState::Pending => {
                assert!(Instant::now() < deadline);
                thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

#[test]
fn cancel_retains_blocked_owner_and_late_data_cannot_restore_current_state() {
    let drops = Arc::new(AtomicUsize::new(0));
    let (entered_tx, entered_rx) = mpsc::channel();
    let (release_tx, release_rx) = mpsc::channel();
    let mut owner = start(
        vec![
            Step::Data(frame(1, false)),
            Step::Gate(entered_tx, release_rx),
        ],
        drops.clone(),
    );
    entered_rx.recv_timeout(Duration::from_secs(2)).unwrap();
    assert!(owner.snapshot().fresh(100.0));
    owner.request_stop();
    assert_eq!(owner.poll_join(), JoinState::Pending);
    assert_eq!(drops.load(Ordering::SeqCst), 0);
    assert_eq!(owner.snapshot().fault, Some(FeedFault::Stopped));
    release_tx.send(frame(2, false)).unwrap();
    assert_eq!(join(&mut owner), WorkerEnd::Canceled);
    assert!(!owner.snapshot().fresh(100.0));
    assert_eq!(drops.load(Ordering::SeqCst), 1);
    owner.request_stop();
    assert_eq!(owner.poll_join(), JoinState::Joined(WorkerEnd::Canceled));
    drop(owner);
    assert_eq!(drops.load(Ordering::SeqCst), 1);
}

#[test]
fn eof_and_explicit_terminal_history_remain_distinct() {
    for terminal in [false, true] {
        let drops = Arc::new(AtomicUsize::new(0));
        let mut owner = start(
            vec![Step::Data(frame(1, terminal)), Step::Eof],
            drops.clone(),
        );
        assert_eq!(join(&mut owner), WorkerEnd::SourceEnded);
        let snapshot = owner.snapshot();
        assert_eq!(snapshot.fault, Some(FeedFault::Eof));
        assert_eq!(snapshot.terminal_observed(), terminal);
        assert!(!snapshot.fresh(100.0));
        assert_eq!(drops.load(Ordering::SeqCst), 1);
    }
}

#[test]
fn io_failure_and_panic_never_become_successful_eof() {
    for (step, expected) in [
        (Step::Error, WorkerEnd::SourceEnded),
        (Step::Panic, WorkerEnd::Panicked),
    ] {
        let drops = Arc::new(AtomicUsize::new(0));
        let mut owner = start(vec![step], drops.clone());
        assert_eq!(join(&mut owner), expected);
        assert_eq!(owner.snapshot().fault, Some(FeedFault::Read));
        assert!(!owner.snapshot().source_closed());
        assert_eq!(drops.load(Ordering::SeqCst), 1);
    }
}

#[test]
fn dropping_an_owned_nonblocking_reader_cancels_and_joins_not_detaches() {
    let drops = Arc::new(AtomicUsize::new(0));
    let owner = start(vec![], drops.clone());
    drop(owner);
    assert_eq!(drops.load(Ordering::SeqCst), 1);
}

#[test]
fn worker_clock_failure_closes_input_without_substituting_cached_time() {
    let drops = Arc::new(AtomicUsize::new(0));
    let calls = Arc::new(AtomicUsize::new(0));
    let clock = move || {
        if calls.fetch_add(1, Ordering::SeqCst) < 3 {
            Some(100.0)
        } else {
            None
        }
    };
    let provider = Provider::new(
        Fake {
            steps: vec![Step::Data(frame(1, false))].into(),
            drops: drops.clone(),
        },
        clock,
    )
    .unwrap();
    let mut owner = ReaderOwner::start(provider).unwrap();
    assert_eq!(join(&mut owner), WorkerEnd::SourceEnded);
    assert_eq!(owner.snapshot().fault, Some(FeedFault::Clock));
    assert_eq!(drops.load(Ordering::SeqCst), 1);
}

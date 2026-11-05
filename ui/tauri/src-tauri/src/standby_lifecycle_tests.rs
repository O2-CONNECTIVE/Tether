use super::*;
use std::sync::mpsc;

fn state_gate(outcome: Option<Outcome>, requested: bool) -> ExitGate {
    let gate = ExitGate::new(CloseHandle::fixture());
    let mut state = gate.0.state.lock().unwrap();
    state.outcome = outcome;
    state.requested = requested;
    drop(state);
    gate
}

#[test]
fn no_request_no_result_unknown_and_unjoined_monitor_never_allow_exit() {
    for outcome in [None, Some(Outcome::Unknown), Some(Outcome::Confirmed)] {
        let gate = state_gate(outcome, false);
        assert!(!gate.finish_requested());
        assert!(!gate.request_exit()); // no actual monitor join
        assert!(!gate.finish_requested());
    }
    assert!(safe(Some(Outcome::StartupRejected)));
    assert!(safe(Some(Outcome::NotSpawned)));
}

#[test]
fn uncontended_monitor_cannot_join_itself() {
    let gate = state_gate(Some(Outcome::Confirmed), true);
    let callback = gate.clone();
    let (start, ready) = mpsc::channel();
    let monitor = thread::spawn(move || {
        ready.recv().unwrap();
        assert!(!callback.finish_requested());
    });
    gate.0.monitor.lock().unwrap().thread = Some(monitor);
    start.send(()).unwrap();
    // Wait without taking the monitor mutex: the callback checks its own ID.
    loop {
        let finished = gate
            .0
            .monitor
            .lock()
            .unwrap()
            .thread
            .as_ref()
            .unwrap()
            .is_finished();
        if finished {
            break;
        }
        thread::yield_now();
    }
    assert!(gate.finish_requested());
}

#[test]
fn consumed_panicking_monitor_invalidates_confirmed_outcome_permanently() {
    let gate = state_gate(Some(Outcome::Confirmed), true);
    gate.0.monitor.lock().unwrap().thread =
        Some(thread::spawn(|| panic!("fixture monitor failure")));
    assert!(!gate.finish_requested());
    assert_eq!(gate.outcome(), Some(Outcome::Unknown));
    assert!(!gate.finish_requested());
    assert!(!gate.request_exit());
}

#[test]
fn same_child_close_and_supervisor_join_precede_outer_join_and_exit() {
    let (producer, source) = Supervisor::retained_close_fixture();
    let first = source.first_with_timeout(Duration::from_secs(2));
    let mut owner = StandbyOwner::start(producer).unwrap();
    let gate = owner.gate.clone();
    let (entered, notified) = mpsc::channel();
    let (release, wait) = mpsc::channel();
    let wait = Mutex::new(wait);
    gate.set_wake(Arc::new(move || {
        entered.send(()).unwrap();
        wait.lock().unwrap().recv().unwrap();
    }));
    let initial = gate.request_exit();
    notified.recv_timeout(Duration::from_secs(5)).unwrap();
    let published = gate.outcome(); // IO, exact Child Wait, supervisor joined
    let cloned = gate.clone();
    let (done, result) = mpsc::channel();
    let exiting = thread::spawn(move || done.send(cloned.finish_requested()).unwrap());
    let before_return = result.recv_timeout(Duration::from_millis(100));
    let start = std::time::Instant::now();
    let contended = loop {
        if matches!(gate.0.monitor.try_lock(), Err(TryLockError::WouldBlock)) {
            break true;
        }
        if start.elapsed() > Duration::from_secs(5) {
            break false;
        }
        thread::yield_now();
    };
    // A second caller cannot treat the taken-but-joining handle as completion.
    let concurrent = contended && gate.finish_requested();
    let (joining, begun) = mpsc::channel();
    let (joined, owner_result) = mpsc::channel();
    let owner_join = thread::spawn(move || {
        joining.send(()).unwrap();
        owner.join();
        joined.send(()).unwrap();
    });
    begun.recv_timeout(Duration::from_secs(5)).unwrap();
    let owner_before_return = owner_result.recv_timeout(Duration::from_millis(100));
    release.send(()).unwrap();
    let after_return = result.recv_timeout(Duration::from_secs(5)).unwrap();
    exiting.join().unwrap();
    owner_result.recv_timeout(Duration::from_secs(5)).unwrap();
    owner_join.join().unwrap();
    assert!(first.is_ok());
    assert!(!initial);
    assert_eq!(published, Some(Outcome::Confirmed));
    assert!(before_return.is_err());
    assert!(owner_before_return.is_err());
    assert!(contended);
    assert!(!concurrent);
    assert!(after_return && gate.finish_requested() && gate.request_exit());
    assert!(gate.0.monitor.lock().unwrap().joined);
}

#[test]
fn dropping_the_outer_owner_closes_and_joins_without_fabricating_exit_intent() {
    let (producer, source) = Supervisor::retained_close_fixture();
    let first = source.first_with_timeout(Duration::from_secs(2));
    let owner = StandbyOwner::start(producer).unwrap();
    let gate = owner.gate.clone();
    drop(source);
    let before = gate.outcome();
    drop(owner);
    assert!(first.is_ok());
    assert_eq!(before, None);
    assert_eq!(gate.outcome(), Some(Outcome::Confirmed));
    assert!(gate.0.monitor.lock().unwrap().joined);
    assert!(!gate.finish_requested());
}

// Only for negative tests: this Supervisor's test guard permits its already
// reaped worker to return. The actual outer join is consumed before releasing
// the sole StandbyOwner field. Production Drop's unknown hold is not bypassed.
fn release_joined_negative(owner: StandbyOwner) -> (Option<Outcome>, bool, bool) {
    let mut owner = std::mem::ManuallyDrop::new(owner);
    let joined = owner.gate.join_monitor();
    let outcome = owner.gate.outcome();
    let can_exit = owner.gate.finish_requested();
    let consumed = owner.gate.0.monitor.lock().unwrap().thread.is_none();
    assert!(
        consumed,
        "negative fixture must consume its actual outer handle"
    );
    // SAFETY: StandbyOwner has only this field. It is dropped exactly once,
    // after the actual outer monitor and owned Child/IO worker were joined.
    unsafe { std::ptr::drop_in_place(&mut owner.gate) };
    (outcome, joined, can_exit)
}

#[test]
fn physical_reap_and_outer_join_do_not_replace_close_authority() {
    for mode in ["unknown", "missing", "exit-error"] {
        let (producer, source) = Supervisor::close_fixture(mode);
        let first = source.first_with_timeout(Duration::from_secs(2));
        let owner = StandbyOwner::start(producer).unwrap();
        assert!(!owner.gate.request_exit());
        let (outcome, joined, can_exit) = release_joined_negative(owner);
        assert!(first.is_ok());
        assert_eq!(outcome, Some(Outcome::Unknown), "{mode}");
        assert!(joined && !can_exit);
    }
}

#[test]
fn actual_monitor_wake_panic_rejects_previously_published_confirmation() {
    let (producer, source) = Supervisor::retained_close_fixture();
    let first = source.first_with_timeout(Duration::from_secs(2));
    let owner = StandbyOwner::start(producer).unwrap();
    owner
        .gate
        .set_wake(Arc::new(|| panic!("fixture wake failure")));
    assert!(!owner.gate.request_exit());
    let (outcome, joined, can_exit) = release_joined_negative(owner);
    assert!(first.is_ok());
    assert_eq!(outcome, Some(Outcome::Unknown));
    assert!(!joined && !can_exit);
}

#[test]
fn late_wake_registration_observes_an_already_completed_exit_request() {
    use std::sync::atomic::{AtomicUsize, Ordering};
    let (producer, source) = Supervisor::retained_close_fixture();
    let first = source.first_with_timeout(Duration::from_secs(2));
    let mut owner = StandbyOwner::start(producer).unwrap();
    owner.gate.request_exit();
    owner.join();
    let count = Arc::new(AtomicUsize::new(0));
    let observed = count.clone();
    owner.gate.set_wake(Arc::new(move || {
        observed.fetch_add(1, Ordering::SeqCst);
    }));
    assert!(first.is_ok());
    assert_eq!(count.load(Ordering::SeqCst), 1);
    assert!(owner.gate.finish_requested());
}

#[test]
fn guarded_success_error_and_unwind_join_the_same_retained_owner() {
    for mode in [0, 1, 2] {
        let (producer, source) = Supervisor::retained_close_fixture();
        let first = source.first_with_timeout(Duration::from_secs(2));
        let mut owner = StandbyOwner::start(producer).unwrap();
        let result = owner.guarded(|| match mode {
            0 => Ok(()),
            1 => Err("fixture setup error"),
            _ => panic!("fixture setup unwind"),
        });
        assert!(first.is_ok());
        assert_eq!(
            result,
            match mode {
                0 => Ok(()),
                1 => Err("fixture setup error"),
                _ => Err("RESIDENT_GUI_UNAVAILABLE"),
            }
        );
        assert_eq!(owner.gate.outcome(), Some(Outcome::Confirmed));
        assert!(owner.gate.0.monitor.lock().unwrap().joined);
        assert!(!owner.gate.finish_requested()); // joined isn't an exit intent
        assert!(owner.gate.request_exit());
    }
}

#[test]
fn no_spawn_is_distinct_from_confirmed_cleanup_but_requires_actual_outer_join() {
    let (producer, source) =
        Supervisor::fixture_retained(std::process::Command::new("/dev/null/not-a-program"))
            .unwrap();
    let mut owner = StandbyOwner::start(producer).unwrap();
    owner.join();
    assert!(source.is_err());
    assert_eq!(owner.gate.outcome(), Some(Outcome::NotSpawned));
    assert!(owner.gate.request_exit());
}

#[test]
fn monitor_self_reentry_declines_even_while_another_join_owns_the_lock() {
    let gate = ExitGate::new(CloseHandle::fixture());
    gate.0.state.lock().unwrap().requested = true;
    gate.complete(Outcome::Confirmed);
    let callback_gate = gate.clone();
    let (start, ready) = mpsc::channel();
    let (done, result) = mpsc::channel();
    let monitor = thread::spawn(move || {
        ready.recv().unwrap();
        done.send(callback_gate.finish_requested()).unwrap();
    });
    gate.0.monitor.lock().unwrap().thread = Some(monitor);
    // Reproduce the lock held by an external join while the monitor calls its
    // wake. Release it before any assertion, so the red test also joins cleanly.
    let lock = gate.0.monitor.lock().unwrap();
    start.send(()).unwrap();
    let before_release = result.recv_timeout(Duration::from_millis(200));
    drop(lock);
    if before_release.is_err() {
        assert!(!result.recv_timeout(Duration::from_secs(2)).unwrap());
    }
    assert!(gate.join_monitor());
    assert_eq!(
        before_release.ok(),
        Some(false),
        "self-monitor callback blocked behind its own join"
    );
}

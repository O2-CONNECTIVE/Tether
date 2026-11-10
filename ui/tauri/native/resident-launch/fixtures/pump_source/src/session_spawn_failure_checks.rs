//! Fixed subprocess contains an intentionally parked GUI-startup caller.
//! No timeout or elapsed sleep is evidence: a receipt marks the real divergent
//! failure branch after the exact monitor closure has dropped its supervisor.
use super::NativeSession;
use crate::native_preparation::{
    ExitObservation, FixedActiveFixture, NativePreparation, Preparation,
};
use crate::standby_lifecycle::native::NativeOuterOwner;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
    mpsc,
};
use std::time::Duration;
pub(super) fn run() {
    let (sender, receiver) = mpsc::sync_channel(1);
    let returned = Arc::new(AtomicBool::new(false));
    let observed = returned.clone();
    let caller = std::thread::spawn(move || {
        NativeOuterOwner::fixture_fail_next_spawn(sender);
        let _unexpected = NativeSession::fixture(FixedActiveFixture::Early, false);
        observed.store(true, Ordering::SeqCst);
    });
    // This bound is failure-only: no receipt, premature return or panic fails.
    receiver
        .recv_timeout(Duration::from_secs(10))
        .expect("containment receipt missing");
    let mut passed = 0;
    let mut check = |condition: bool| {
        assert!(condition, "session condition {}", passed + 1);
        passed += 1;
    };
    check(!returned.load(Ordering::SeqCst));
    check(!caller.is_finished());
    check(NativePreparation::fixture_counts().open == 0);
    check(NativePreparation::fixture_counts().closes == 4);
    check(NativePreparation::fixture_active_stats().waits == 1);
    check(NativePreparation::fixture_retained_state() == 3);
    check(matches!(
        NativePreparation::fixture_active(FixedActiveFixture::Early),
        Preparation::Refused
    ));
    check(NativePreparation::fixture_collect_retained_active() == ExitObservation::Unavailable);
    check(!returned.load(Ordering::SeqCst));
    // The native child was already waited and no native worker remains. The
    // intentionally divergent caller is confined to this one fixture process;
    // returning main terminates it, never pretending it joined or allowed Quit.
    drop(caller);
    println!(
        "NATIVE_SESSION_FIXTURE mode=session-monitor-spawn-failure passed={passed} failed=0 childrenStarted=1 childrenWaited=1 open=0 nativeUnknown=1 producer=0"
    );
}

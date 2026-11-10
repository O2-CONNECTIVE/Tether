//! A real no-owner receipt cannot permit Quit before the actual outer join.
use super::{NativeIoSupervisor, NativeOutcome, NativeOuterOwner, Outcome, join, pause};
use crate::native_preparation::NativePreparation;
pub(super) fn run(mode: &str) {
    let panic = mode == "outer-startup-panic";
    let (producer, source) = NativeIoSupervisor::prepare().unwrap();
    let (owner, finish) = NativeOuterOwner::fixture_held(producer, panic).unwrap();
    let mut passed = 0;
    let mut check = |condition: bool| {
        assert!(condition, "startup outer condition {}", passed + 1);
        passed += 1;
    };
    check(source.is_err());
    while !finish.ready() {
        pause();
    }
    check(owner.poll_join().is_none());
    check(owner.outcome() == Some(Outcome::Unknown));
    check(!owner.request_exit());
    check(!owner.finish_requested());
    check(owner.fixture_contended_join_refuses());
    finish.release();
    if panic {
        while !owner.fixture_failed_join() {
            let _ = owner.poll_join();
            pause();
        }
        let no_proof = owner.poll_join().is_none();
        let outcome = owner.outcome();
        let exit = owner.request_exit();
        let finished = owner.finish_requested();
        // Failed join deliberately retains the unknown guard forever. All
        // native work is already joined; this fixed subprocess owns no child.
        std::mem::forget(owner);
        check(no_proof);
        check(outcome == Some(Outcome::Unknown));
        check(!exit);
        check(!finished);
    } else {
        let joined = join(&owner);
        check(joined.native().outcome() == NativeOutcome::UnavailableBeforeOwnership);
        check(!joined.wire_close_confirmed() && !joined.native().terminal_confirmed());
        check(owner.outcome() == Some(Outcome::NotSpawned));
        check(owner.request_exit() && owner.finish_requested());
        drop(owner);
    }
    check(NativePreparation::fixture_counts().open == 0);
    check(NativePreparation::fixture_active_stats().waits == 0);
    println!(
        "NATIVE_OUTER_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=0 childrenWaited=0 open=0 nativeUnknown=0 producer=0"
    );
}

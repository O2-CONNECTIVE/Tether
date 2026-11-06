//! Real native IO → original outer monitor; no full cleanup/Quit permit.
use super::owner::native::{NativeIoSupervisor, NativeOutcome};
use crate::standby_lifecycle::{
    native::{NativeOuterJoined, NativeOuterOwner},
    Outcome,
};
#[cfg(resident_launch_fixture)]
use crate::{
    native_preparation::{ExitObservation, FixedActiveFixture, NativePreparation, State},
    resident_port::{model::Disposition, observation::Standby},
};
fn pause() {
    std::thread::sleep(std::time::Duration::from_millis(1));
}
fn join(owner: &NativeOuterOwner) -> NativeOuterJoined {
    loop {
        if let Some(value) = owner.poll_join() {
            return value;
        }
        pause();
    }
}
pub(crate) fn run(mode: &str) {
    if mode == "outer-normal" {
        let (producer, source) = NativeIoSupervisor::prepare().unwrap();
        let owner = NativeOuterOwner::start(producer).unwrap();
        let mut passed = 0;
        let mut check = |v: bool| {
            assert!(v);
            passed += 1;
        };
        check(source.is_err());
        check(!owner.request_exit());
        let joined = join(&owner);
        check(joined.native().outcome() == NativeOutcome::Refused);
        check(!joined.wire_close_confirmed());
        check(owner.outcome() == Some(Outcome::Unknown));
        check(!owner.finish_requested());
        check(!owner.request_exit());
        check(owner.poll_join() == Some(joined));
        drop(owner);
        drop(source);
        println!("NATIVE_OUTER_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=0 childrenWaited=0 open=0 nativeUnknown=0 producer=0");
        return;
    }
    #[cfg(resident_launch_fixture)]
    active(mode);
    #[cfg(not(resident_launch_fixture))]
    panic!("active fixture unavailable");
}
#[cfg(resident_launch_fixture)]
fn active(mode: &str) {
    if mode == "outer-prepared" {
        let (producer, source) = NativeIoSupervisor::fixture_prepared().unwrap();
        let owner = NativeOuterOwner::start(producer).unwrap();
        let mut passed = 0;
        let mut check = |v: bool| {
            assert!(v);
            passed += 1;
        };
        check(source.is_err());
        check(!owner.request_exit());
        let joined = join(&owner);
        check(joined.native().outcome() == NativeOutcome::Prepared(State::RetiredUnlaunched));
        check(!joined.wire_close_confirmed());
        check(owner.outcome() == Some(Outcome::Unknown));
        check(!owner.finish_requested());
        check(!owner.request_exit());
        check(owner.poll_join() == Some(joined));
        drop(owner);
        drop(source);
        check(NativePreparation::fixture_counts().open == 0);
        check(NativePreparation::fixture_counts().closes == 4);
        check(NativePreparation::fixture_counts().retire_calls == 1);
        check(NativePreparation::fixture_active_stats().waits == 0);
        check(NativePreparation::fixture_retained_state() == 0);
        println!("NATIVE_OUTER_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=0 childrenWaited=0 open=0 nativeUnknown=0 producer=0");
        return;
    }
    let (fixture, id) = match mode {
        "outer-confirmed" => (FixedActiveFixture::Echo, Some(1)),
        "outer-unknown" => (FixedActiveFixture::Echo, Some(2)),
        "outer-missing" => (FixedActiveFixture::Echo, Some(3)),
        "outer-malformed" => (FixedActiveFixture::Echo, Some(4)),
        "outer-invalid" => (FixedActiveFixture::Early, None),
        "outer-rejected" => (FixedActiveFixture::ExitTwo, None),
        "outer-signal" => (FixedActiveFixture::Signal, None),
        "outer-lost-receipt" => (FixedActiveFixture::LostCloseReceipt, None),
        "outer-unwind" | "outer-drop" | "outer-held" | "outer-panic" => {
            (FixedActiveFixture::Echo, None)
        }
        _ => panic!("unknown outer fixture"),
    };
    let (producer, source) = NativeIoSupervisor::fixture(fixture, mode == "outer-unwind").unwrap();
    let first = source.as_ref().ok().and_then(|view| view.first().ok());
    let (owner, finish) = if matches!(mode, "outer-held" | "outer-panic") {
        let (o, f) = NativeOuterOwner::fixture_held(producer, mode == "outer-panic").unwrap();
        (o, Some(f))
    } else {
        (NativeOuterOwner::start(producer).unwrap(), None)
    };
    let mut passed = 0;
    let mut check = |v: bool| {
        assert!(v, "outer condition {}", passed + 1);
        passed += 1;
    };
    if mode == "outer-panic" {
        // Finish/Wait/close before monitor panic; forget the retained unknown
        // parent BEFORE assertions so a negative never abandons a live child.
        let exit_before = owner.request_exit();
        let f = finish.unwrap();
        while !f.ready() {
            pause();
        }
        let no_early_proof = owner.poll_join().is_none();
        let contended = owner.fixture_contended_join_refuses();
        f.release();
        while !owner.fixture_failed_join() {
            let _ = owner.poll_join();
            pause();
        }
        let no_proof = owner.poll_join().is_none();
        let verdict = owner.outcome();
        let exit_after = owner.request_exit();
        let finish_after = owner.finish_requested();
        std::mem::forget(owner);
        drop(source);
        check(first.is_some());
        check(!exit_before);
        check(no_early_proof);
        check(contended);
        check(no_proof);
        check(verdict == Some(Outcome::Unknown));
        check(!exit_after);
        check(!finish_after);
    } else {
        check(!owner.finish_requested());
        if let Some(id) = id {
            check(first.is_some());
            check(owner.poll_join().is_none());
            let source = source.as_ref().unwrap();
            let response = source
                .request(super::test_frames::intent_request(id, 1))
                .unwrap()
                .recv_timeout(std::time::Duration::from_secs(3))
                .unwrap();
            check(response.request_id == id && response.disposition == Disposition::Accepted);
            check(source.current().is_some_and(|s| s.standby == Standby::Off));
        }
        if mode == "outer-drop" {
            check(first.is_some());
            check(NativePreparation::fixture_active_stats().waits == 0);
            drop(owner);
            check(
                source
                    .as_ref()
                    .unwrap()
                    .current()
                    .is_some_and(|s| s.valid_for_ms == 0),
            );
        } else {
            check(!owner.request_exit());
            if let Some(f) = finish {
                while !f.ready() {
                    pause();
                }
                check(owner.poll_join().is_none());
                check(owner.outcome() == Some(Outcome::Unknown));
                check(!owner.finish_requested());
                check(owner.fixture_contended_join_refuses());
                f.release();
            }
            let joined = join(&owner);
            let exit = match mode {
                "outer-rejected" => ExitObservation::Exit(2),
                "outer-signal" => ExitObservation::Signal(15),
                _ => ExitObservation::Exit(0),
            };
            let io = if matches!(
                mode,
                "outer-invalid" | "outer-lost-receipt" | "outer-unwind"
            ) {
                State::Unknown
            } else {
                State::ActiveIoRetired
            };
            check(
                joined.native().outcome()
                    == NativeOutcome::Active {
                        exit,
                        io,
                        unwound: mode == "outer-unwind",
                    },
            );
            check(
                joined.wire_close_confirmed() == matches!(mode, "outer-confirmed" | "outer-held"),
            );
            check(owner.outcome() == Some(Outcome::Unknown));
            check(!owner.finish_requested());
            check(!owner.request_exit());
            check(owner.poll_join() == Some(joined));
            drop(owner);
        }
        drop(source);
    }
    check(NativePreparation::fixture_counts().open == 0);
    check(NativePreparation::fixture_counts().closes == 4);
    check(NativePreparation::fixture_counts().retire_calls == 0);
    check(NativePreparation::fixture_active_stats().waits == 1);
    check(
        NativePreparation::fixture_active_stats().abandons
            == if mode == "outer-lost-receipt" { 3 } else { 2 },
    );
    check(NativePreparation::fixture_retained_state() == 3);
    check(matches!(
        NativePreparation::fixture_active(FixedActiveFixture::Early),
        crate::native_preparation::Preparation::Refused
    ));
    check(NativePreparation::fixture_collect_retained_active() == ExitObservation::Unavailable);
    println!("NATIVE_OUTER_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=1 childrenWaited=1 open=0 nativeUnknown=1 producer=0");
}

#[cfg(resident_launch_fixture)]
#[path = "discard_checks.rs"]
mod discard_checks;
use super::owner::native::{NativeIoSupervisor, NativeJoined, NativeOutcome};
#[cfg(resident_launch_fixture)]
use crate::native_preparation::{ExitObservation, NativePreparation, State};
#[cfg(resident_launch_fixture)]
use crate::{
    native_preparation::FixedActiveFixture,
    resident_port::{model::Disposition, observation::Standby},
};
fn join(supervisor: &mut NativeIoSupervisor) -> NativeJoined {
    loop {
        if let Some(value) = supervisor.poll_join() {
            return value;
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
}
pub(crate) fn run(mode: &str) {
    if mode == "supervisor-normal" {
        let (mut supervisor, source) = NativeIoSupervisor::prepare().unwrap();
        let mut passed = 0;
        let mut check = |v: bool| {
            assert!(v, "native supervisor condition {}", passed + 1);
            passed += 1;
        };
        check(source.is_err());
        check(supervisor.joined().is_none());
        check(!supervisor.wire_close_confirmed());
        let actual = join(&mut supervisor);
        check(actual.outcome() == NativeOutcome::Refused);
        check(supervisor.joined() == Some(actual));
        check(supervisor.poll_join().is_none());
        check(!supervisor.wire_close_confirmed());
        drop(supervisor);
        println!(
            "NATIVE_SUPERVISOR_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=0 childrenWaited=0 open=0 unknown=0 producer=0"
        );
        return;
    }
    #[cfg(resident_launch_fixture)]
    active(mode);
    #[cfg(not(resident_launch_fixture))]
    panic!("active fixture unavailable");
}
#[cfg(resident_launch_fixture)]
fn active(mode: &str) {
    if matches!(
        mode,
        "supervisor-discard-trailing" | "supervisor-discard-pending"
    ) {
        discard_checks::run(mode);
        return;
    }
    if mode == "supervisor-prepared" {
        let (mut supervisor, source) = NativeIoSupervisor::fixture_prepared().unwrap();
        let mut passed = 0;
        let mut check = |v: bool| {
            assert!(v, "native supervisor condition {}", passed + 1);
            passed += 1;
        };
        check(source.is_err());
        check(supervisor.joined().is_none());
        check(!supervisor.wire_close_confirmed());
        let actual = join(&mut supervisor);
        check(actual.outcome() == NativeOutcome::Prepared(State::RetiredUnlaunched));
        check(supervisor.joined() == Some(actual));
        check(supervisor.poll_join().is_none());
        check(!supervisor.wire_close_confirmed());
        drop(supervisor);
        check(NativePreparation::fixture_counts().open == 0);
        check(NativePreparation::fixture_counts().closes == 4);
        check(NativePreparation::fixture_counts().retire_calls == 1);
        check(NativePreparation::fixture_active_stats().waits == 0);
        check(NativePreparation::fixture_active_stats().abandons == 0);
        check(NativePreparation::fixture_retained_state() == 0);
        println!(
            "NATIVE_SUPERVISOR_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=0 childrenWaited=0 open=0 unknown=0 producer=0"
        );
        return;
    }
    let (fixture, id) = match mode {
        "supervisor-finite-coalesced" => (FixedActiveFixture::FiniteCoalesced, None),
        "supervisor-finite-malformed" => (FixedActiveFixture::FiniteMalformed, None),
        "supervisor-finite-unwind" => (FixedActiveFixture::FiniteUnwind, None),
        "supervisor-finite-unknown" => (FixedActiveFixture::FiniteUnknown, None),
        "supervisor-confirmed" => (FixedActiveFixture::Echo, Some(1)),
        "supervisor-unknown" => (FixedActiveFixture::Echo, Some(2)),
        "supervisor-missing" => (FixedActiveFixture::Echo, Some(3)),
        "supervisor-malformed" => (FixedActiveFixture::Echo, Some(4)),
        "supervisor-invalid" => (FixedActiveFixture::Early, None),
        "supervisor-rejected" => (FixedActiveFixture::ExitTwo, None),
        "supervisor-signal" => (FixedActiveFixture::Signal, None),
        "supervisor-lost-receipt" => (FixedActiveFixture::LostCloseReceipt, None),
        "supervisor-unwind" | "supervisor-drop" => (FixedActiveFixture::Echo, None),
        _ => panic!("unknown native supervisor fixture"),
    };
    let (mut supervisor, source) = NativeIoSupervisor::fixture(
        fixture,
        matches!(mode, "supervisor-unwind" | "supervisor-finite-unwind"),
    )
    .unwrap();
    let mut passed = 0;
    let mut check = |v: bool| {
        assert!(v, "native supervisor condition {}", passed + 1);
        passed += 1;
    };
    check(supervisor.joined().is_none());
    check(!supervisor.wire_close_confirmed());
    let before_close = source.as_ref().ok().and_then(|view| view.first().ok());
    if let Some(id) = id {
        check(before_close.is_some());
        let source = source.as_ref().unwrap();
        let response = source
            .request(super::test_frames::intent_request(id, 1))
            .unwrap()
            .recv_timeout(std::time::Duration::from_secs(3))
            .unwrap();
        check(response.request_id == id && response.disposition == Disposition::Accepted);
        check(
            source
                .current()
                .is_some_and(|status| status.standby == Standby::Off),
        );
        check(supervisor.poll_join().is_none()); // Child still awaits control EOF.
    }
    if mode == "supervisor-drop" {
        check(before_close.is_some());
        check(NativePreparation::fixture_active_stats().waits == 0);
        // Dropping the parent requests Close and joins its original IO worker.
        // The view remains alive; its existence does not own process shutdown.
        drop(supervisor);
        check(
            source
                .as_ref()
                .unwrap()
                .current()
                .is_some_and(|status| status.valid_for_ms == 0),
        );
    } else {
        supervisor.request_close_transport();
        if mode == "supervisor-confirmed" {
            // Completion/TLS receipts alone cannot mint the parent join proof.
            while NativePreparation::fixture_active_stats().abandons < 2 {
                std::thread::sleep(std::time::Duration::from_millis(1));
            }
            check(supervisor.joined().is_none());
            check(!supervisor.wire_close_confirmed());
        }
        let actual = join(&mut supervisor);
        if mode.starts_with("supervisor-finite-") {
            if let NativeOutcome::Active {
                exit: ExitObservation::Exit(code),
                ..
            } = actual.outcome()
            {
                eprintln!("NATIVE_SUPERVISOR_CHILD_EXIT {code}");
            }
        }
        let expected_exit = match mode {
            "supervisor-rejected" => ExitObservation::Exit(2),
            "supervisor-signal" => ExitObservation::Signal(15),
            _ => ExitObservation::Exit(0),
        };
        let expected_io = if matches!(
            mode,
            "supervisor-invalid"
                | "supervisor-lost-receipt"
                | "supervisor-unwind"
                | "supervisor-finite-coalesced"
                | "supervisor-finite-malformed"
                | "supervisor-finite-unwind"
                | "supervisor-finite-unknown"
        ) {
            State::Unknown
        } else {
            State::ActiveIoRetired
        };
        check(
            actual.outcome()
                == NativeOutcome::Active {
                    exit: expected_exit,
                    io: expected_io,
                    unwound: matches!(mode, "supervisor-unwind" | "supervisor-finite-unwind"),
                },
        );
        check(supervisor.joined() == Some(actual));
        check(supervisor.poll_join().is_none()); // No second join or new owner.
        check(supervisor.wire_close_confirmed() == (mode == "supervisor-confirmed"));
        drop(supervisor);
    }
    drop(source);
    check(NativePreparation::fixture_counts().open == 0);
    check(NativePreparation::fixture_counts().closes == 4);
    check(NativePreparation::fixture_counts().retire_calls == 0);
    check(NativePreparation::fixture_active_stats().waits == 1);
    let abandons = if matches!(
        mode,
        "supervisor-invalid"
            | "supervisor-unwind"
            | "supervisor-lost-receipt"
            | "supervisor-finite-unknown"
            | "supervisor-finite-coalesced"
            | "supervisor-finite-malformed"
            | "supervisor-finite-unwind"
    ) {
        3
    } else {
        2
    };
    check(NativePreparation::fixture_active_stats().abandons == abandons);
    check(NativePreparation::fixture_retained_state() == 3);
    check(matches!(
        NativePreparation::fixture_active(FixedActiveFixture::Early),
        crate::native_preparation::Preparation::Refused
    ));
    check(NativePreparation::fixture_collect_retained_active() == ExitObservation::Unavailable);
    println!(
        "NATIVE_SUPERVISOR_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=1 childrenWaited=1 open=0 unknown=1 producer=0"
    );
}

//! Actual Pump/shared IO loop; each worker retains its original affine owner.
use super::{exchange::Shared, pump::Pump};
use crate::mac_status::ContinuousClock;
use crate::native_preparation::{
    ExitObservation, FixedActiveFixture, NativePreparation, Preparation, State,
};
use crate::resident_port::{model::Disposition, observation::Standby};
use std::sync::{Arc, Mutex};

fn pause() {
    std::thread::sleep(std::time::Duration::from_millis(1));
}
fn wait(mut poll: impl FnMut() -> ExitObservation) -> ExitObservation {
    loop {
        let value = poll();
        if value != ExitObservation::Pending {
            return value;
        }
        pause();
    }
}
fn locked(shared: &Mutex<Shared>) -> std::sync::MutexGuard<'_, Shared> {
    shared
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner)
}

// Bounded fixed fixture output only. This is not an unbounded producer drainer.
fn finish(owner: &mut NativePreparation) -> (ExitObservation, State) {
    if owner.state() == State::Active {
        let _ = owner.io().half_close_control();
    }
    let exit = wait(|| owner.poll_exit());
    (exit, owner.retire_active_io())
}

pub(crate) fn run(mode: &str) {
    let (fixture, id) = match mode {
        "active-confirmed" => (FixedActiveFixture::Echo, Some(1)),
        "active-unknown" => (FixedActiveFixture::Echo, Some(2)),
        "active-missing" => (FixedActiveFixture::Echo, Some(3)),
        "active-malformed" => (FixedActiveFixture::Echo, Some(4)),
        "active-invalid" => (FixedActiveFixture::Early, None),
        "active-rejected" => (FixedActiveFixture::ExitTwo, None),
        "active-signal" => (FixedActiveFixture::Signal, None),
        "active-lost-receipt" => (FixedActiveFixture::LostCloseReceipt, None),
        "active-panic" => (FixedActiveFixture::Echo, None),
        _ => panic!("unknown active Pump case"),
    };
    let panic_case = mode == "active-panic";
    let shared = Arc::new(Mutex::new(Shared::default()));
    let worker_shared = shared.clone();
    let worker = std::thread::spawn(move || {
        let preparation = NativePreparation::fixture_active(fixture);
        let mut original = match preparation {
            Preparation::Owned(owner) => owner,
            Preparation::Unknown(mut owner) => {
                assert!(Pump::take_active(&mut owner).is_err());
                assert!(Pump::drive_active_io(
                    &mut owner,
                    ContinuousClock::new().unwrap(),
                    worker_shared
                )
                .is_err());
                drop(owner); // Original active Drop, never prelaunch retire.
                let exit = wait(NativePreparation::fixture_collect_retained_active);
                return (exit, State::Unknown);
            }
            Preparation::Refused => panic!("active owner refused"),
        };
        // No active result may borrow the prelaunch Pump or retire its owner.
        assert!(Pump::take_prepared(&mut original).is_err());
        assert_eq!(original.retire(), State::Active);
        assert_eq!(original.retire_active_io(), State::Active); // Wait not observed.
        let clock = ContinuousClock::new().unwrap();
        let outcome = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            if panic_case {
                let mut pump = Pump::take_active(&mut original).unwrap();
                super::owner::drive_io(
                    |state, clock| {
                        let _ = pump.step(state, clock);
                        panic!("fixed active IO unwind");
                    },
                    clock,
                    worker_shared,
                );
            } else {
                Pump::drive_active_io(&mut original, clock, worker_shared).unwrap();
            }
        }));
        // Unwind and parser failure still use this original guard on this TLS
        // worker. No replacement owner, external signal or second child.
        let result = finish(&mut original);
        assert!(Pump::take_active(&mut original).is_err());
        assert!(matches!(
            NativePreparation::fixture_active(FixedActiveFixture::Early),
            Preparation::Refused
        ));
        drop(original);
        if let Err(payload) = outcome {
            std::panic::resume_unwind(payload);
        }
        result
    });
    let clock = ContinuousClock::new().unwrap();
    let start = std::time::Instant::now();
    let ready = loop {
        if locked(&shared)
            .current(clock.now_millis().unwrap())
            .is_some()
        {
            break true;
        }
        if worker.is_finished() || start.elapsed() >= std::time::Duration::from_secs(15) {
            break false;
        }
        pause();
    };
    // Never assert before requesting the original worker's graceful close.
    let receipt = if let Some(id) = id.filter(|_| ready) {
        let receive = locked(&shared).submit(
            super::test_frames::intent_request(id, 1),
            clock.now_millis().unwrap(),
        );
        receive.recv_timeout(std::time::Duration::from_secs(3)).ok()
    } else {
        None
    };
    let stayed_off = locked(&shared)
        .current(clock.now_millis().unwrap())
        .is_some_and(|status| status.standby == Standby::Off);
    locked(&shared).close_control(clock.now_millis().unwrap());
    let result = worker.join(); // Same actual IO worker, including unwind.
    let mut passed = 0;
    let mut check = |value: bool| {
        assert!(value, "active Pump condition {}", passed + 1);
        passed += 1;
    };
    check(result.is_err() == panic_case);
    if let Some(id) = id {
        check(ready);
        check(receipt.is_some_and(|value| {
            value.request_id == id && value.disposition == Disposition::Accepted
        }));
        check(stayed_off); // Receipt is acceptance, not applied ON.
    }
    if !panic_case {
        let (exit, io) = result.unwrap();
        let expected_exit = match mode {
            "active-rejected" => ExitObservation::Exit(2),
            "active-signal" => ExitObservation::Signal(15),
            _ => ExitObservation::Exit(0),
        };
        check(exit == expected_exit);
        let expected_io = if matches!(mode, "active-invalid" | "active-lost-receipt") {
            State::Unknown
        } else {
            State::ActiveIoRetired
        };
        check(io == expected_io);
    }
    check(locked(&shared).closure.confirmed() == (mode == "active-confirmed"));
    check(locked(&shared).startup.confirmed() == (mode == "active-rejected"));
    check(NativePreparation::fixture_counts().open == 0);
    check(NativePreparation::fixture_counts().closes == 4);
    check(NativePreparation::fixture_counts().retire_calls == 0);
    check(NativePreparation::fixture_active_stats().waits == 1);
    check(NativePreparation::fixture_active_stats().abandons == 2);
    check(NativePreparation::fixture_retained_state() == 3);
    check(matches!(
        NativePreparation::fixture_active(FixedActiveFixture::Early),
        Preparation::Refused
    ));
    // The original worker is gone: no foreign-thread collection or recertify.
    check(NativePreparation::fixture_collect_retained_active() == ExitObservation::Unavailable);
    println!("NATIVE_ACTIVE_PUMP_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=1 childrenWaited=1 open=0 unknown=1 producer=0");
}

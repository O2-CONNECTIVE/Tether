//! Actual Rust worker join over the original unlaunched FFI owner only.
use super::{exchange::Shared, owner, pump::Pump};
use crate::mac_status::ContinuousClock;
use crate::native_preparation::{Counts, FixedFixture, NativePreparation, Preparation, State};
use std::sync::{Arc, Mutex};

pub(super) fn run(mode: &str) -> u32 {
    let mode = match mode {
        "worker-eof" => 0,
        "worker-invalid" => 1,
        "worker-rejection" => 2,
        "worker-panic" => 3,
        "worker-unknown" => 4,
        _ => panic!("unknown worker fixture"),
    };
    let shared = Arc::new(Mutex::new(Shared::default()));
    let worker_shared = shared.clone();
    let worker = std::thread::spawn(move || {
        let Preparation::Owned(mut original) = NativePreparation::fixture(if mode == 4 {
            FixedFixture::LostCloseReceipt
        } else {
            FixedFixture::Normal
        }) else {
            panic!("fixture owner refused")
        };
        assert!(original.fixture_probe()); // Same Process, no executable/argv/run.
        let clock = ContinuousClock::new().unwrap();
        {
            let mut pump = Pump::take_prepared(&mut original).unwrap();
            match mode {
                1 => assert!(pump.fixture_peer(0)), // Fixed malformed JSON.
                2 => {
                    let frame = format!("{{\"protocolVersion\":1,\"type\":\"startupRejected\",\"sourceContinuousMs\":{},\"code\":\"policy-rejected\"}}\n", clock.now_millis().unwrap());
                    assert_eq!(pump.fixture_send(frame.as_bytes()), frame.len());
                }
                4 => worker_shared
                    .lock()
                    .unwrap()
                    .close_control(clock.now_millis().unwrap()),
                _ => (),
            }
            if mode == 1 || mode == 2 {
                assert!(pump.fixture_peer(2));
            }
            if mode == 4 {
                // Keep both peer ends open: the fault belongs to the driver's
                // parent half-close, not a successful peer close beforehand.
                assert_eq!(NativePreparation::fixture_counts().open, 4);
            }
            let mut first = true;
            owner::drive_io(
                |state, clock| {
                    let result = pump.step(state, clock);
                    if mode == 0 && first {
                        assert!(matches!(result, Ok(false))); // Actual pending IO.
                        assert!(pump.fixture_peer(2));
                        first = false; // The next driver iteration must see EOF.
                    }
                    if mode == 3 {
                        panic!("synthetic unlaunched worker unwind");
                    }
                    result
                },
                clock,
                worker_shared,
            );
        } // Borrow ends before the SAME owner is retired, still on its thread.
        if mode == 4 {
            assert_eq!(original.state(), State::Unknown);
            assert_eq!(original.retire(), State::Unknown);
            State::Unknown
        } else {
            assert_eq!(original.state(), State::OwnedUnlaunched);
            original.retire()
        }
    });
    // Taking or observing a finished handle is not a join. Always join this one.
    let outcome = worker.join();
    let mut passed = 0;
    let mut check = |condition: bool| {
        assert!(condition);
        passed += 1;
    };
    match mode {
        3 => check(outcome.is_err()),
        4 => check(matches!(outcome, Ok(State::Unknown))),
        _ => check(matches!(outcome, Ok(State::RetiredUnlaunched))),
    }
    let state = shared.lock().unwrap_or_else(|e| e.into_inner());
    check(state.tracking.failed);
    check(!state.closure.confirmed());
    check(state.startup.confirmed() == (mode == 2));
    check(state.waiting.is_none());
    check(state.output.is_none());
    check(
        NativePreparation::fixture_counts()
            == Counts {
                open: 0,
                closes: 4,
                retire_calls: if mode == 4 { 0 } else { 1 },
            },
    );
    if mode == 4 {
        // Thread join cannot replace or silently clear the original unknown slot.
        check(matches!(
            NativePreparation::fixture(FixedFixture::Normal),
            Preparation::Refused
        ));
        check(NativePreparation::fixture_counts().closes == 4);
    }
    passed
}

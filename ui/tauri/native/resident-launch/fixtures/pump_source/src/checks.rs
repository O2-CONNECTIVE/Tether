use super::{exchange::Shared, pump::Pump};
use crate::mac_status::ContinuousClock;
use crate::native_preparation::{Counts, FixedFixture, NativePreparation, Preparation, State};

pub(crate) fn run(mode: &str) -> u32 {
    let Preparation::Owned(mut owner) =
        NativePreparation::fixture(if mode == "half-close-unknown" {
            FixedFixture::LostCloseReceipt
        } else {
            FixedFixture::Normal
        })
    else {
        panic!("native preparation refused")
    };
    let clock = ContinuousClock::new().unwrap();
    let mut shared = Shared::default();
    let mut passed = 0;
    let mut check = |condition: bool| {
        assert!(condition, "native Pump condition {}", passed + 1);
        passed += 1;
    };
    {
        let mut pump = Pump::take_prepared(&mut owner).unwrap();
        check(matches!(pump.step(&mut shared, &clock), Ok(false)));
        check(shared.current(clock.now_millis().unwrap()).is_none());
        match mode {
            "intent" => {
                let frame = format!(
                    "{}\n",
                    super::test_frames::status(clock.now_millis().unwrap())
                );
                check(pump.fixture_send(frame.as_bytes()) == frame.len());
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                let receive = shared.submit(
                    super::test_frames::intent_request(1, 1),
                    clock.now_millis().unwrap(),
                );
                check(shared.output.is_some());
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(shared.output.is_none());
                check(matches!(
                    receive.try_recv(),
                    Err(std::sync::mpsc::TryRecvError::Empty)
                ));
                let mut bytes = [0; 4096];
                let count = pump.fixture_receive(&mut bytes);
                let request: serde_json::Value = serde_json::from_slice(&bytes[..count]).unwrap();
                check(
                    request["type"] == "intent"
                        && request["request"]["requestId"] == 1
                        && request["request"]["intent"] == "on",
                );
                let receipt = super::test_frames::receipt()
                    .to_string()
                    .replace("\"off\"", "\"on\"")
                    + "\n";
                check(pump.fixture_send(receipt.as_bytes()) == receipt.len());
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(matches!(
                    receive.try_recv().unwrap().disposition,
                    crate::resident_port::model::Disposition::Accepted
                ));
                check(matches!(
                    shared.current(clock.now_millis().unwrap()).unwrap().standby,
                    crate::resident_port::observation::Standby::Off
                ));
                check(!shared.closure.confirmed());
            }
            "partial-rejection" => {
                let frame = format!(
                    "{{\"protocolVersion\":1,\"type\":\"startupRejected\",\"sourceContinuousMs\":{},\"code\":\"policy-rejected\"}}\n",
                    clock.now_millis().unwrap()
                );
                check(pump.fixture_send(&frame.as_bytes()[..1]) == 1);
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(!shared.startup.received());
                check(pump.fixture_send(&frame.as_bytes()[1..]) == frame.len() - 1);
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(shared.startup.received());
                check(pump.fixture_peer(2));
                check(matches!(pump.step(&mut shared, &clock), Ok(true)));
                check(shared.startup.confirmed());
                check(!shared.closure.confirmed());
            }
            "discard" => {
                shared.close_control(clock.now_millis().unwrap());
                check(pump.fixture_peer(0));
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(shared.tracking.failed);
                check(pump.fixture_peer(2));
                check(matches!(pump.step(&mut shared, &clock), Ok(true)));
                check(!shared.closure.confirmed());
            }
            "invalid" => {
                check(pump.fixture_peer(0));
                check(pump.step(&mut shared, &clock).is_err());
                shared.fault();
                check(!shared.closure.confirmed());
            }
            "rejection" => {
                let frame = format!(
                    "{{\"protocolVersion\":1,\"type\":\"startupRejected\",\"sourceContinuousMs\":{},\"code\":\"policy-rejected\"}}\n",
                    clock.now_millis().unwrap()
                );
                check(pump.fixture_send(frame.as_bytes()) == frame.len());
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(shared.startup.received());
                check(!shared.startup.confirmed());
                check(pump.fixture_peer(2));
                check(matches!(pump.step(&mut shared, &clock), Ok(true)));
                check(shared.startup.confirmed()); // Parser evidence only; no ExitStatus.
                check(!shared.closure.confirmed());
            }
            "eof" => {
                check(pump.fixture_peer(2));
                check(matches!(pump.step(&mut shared, &clock), Ok(true)));
                check(!shared.closure.confirmed());
                check(!shared.startup.confirmed());
            }
            "half-close" => {
                shared.close_control(clock.now_millis().unwrap());
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(pump.fixture_peer(3));
                check(NativePreparation::fixture_counts().closes == 1);
                check(matches!(pump.step(&mut shared, &clock), Ok(false)));
                check(NativePreparation::fixture_counts().closes == 1);
                check(pump.fixture_peer(2));
                check(matches!(pump.step(&mut shared, &clock), Ok(true)));
                check(!shared.closure.confirmed());
            }
            "half-close-unknown" => {
                shared.close_control(clock.now_millis().unwrap());
                check(pump.step(&mut shared, &clock).is_err());
                check(!shared.closure.confirmed());
                check(
                    NativePreparation::fixture_counts()
                        == Counts {
                            open: 0,
                            closes: 4,
                            retire_calls: 0,
                        },
                );
            }
            _ => panic!("unknown native Pump fixture"),
        }
    }
    if mode == "half-close-unknown" {
        check(owner.state() == State::Unknown);
        check(owner.retire() == State::Unknown);
        check(matches!(
            NativePreparation::fixture(FixedFixture::Normal),
            Preparation::Refused
        ));
    } else {
        check(owner.state() == State::OwnedUnlaunched);
        check(owner.retire() == State::RetiredUnlaunched);
    }
    check(NativePreparation::fixture_counts().open == 0);
    passed
}

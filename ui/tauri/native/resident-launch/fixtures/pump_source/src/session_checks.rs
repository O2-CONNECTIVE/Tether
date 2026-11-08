//! Exact native owner + actual typed port; synthetic status is not auth proof.
use super::native_session::NativeSession;
#[cfg(resident_launch_fixture)]
use crate::native_preparation::{FixedActiveFixture, NativePreparation, Preparation};
use crate::resident_port::duplex::owner::native::NativeOutcome;
use crate::standby_lifecycle::{Outcome, native::NativeOuterJoined};
use serde_json::{Value, json};
fn status(session: &NativeSession) -> Value {
    serde_json::to_value(session.port().current()).unwrap()
}
fn intent(id: u64) -> Value {
    json!({"protocolVersion":1,"requestId":id,"expectedRevision":1,"intent":"on"})
}
fn receipt(session: &NativeSession, id: u64) -> Value {
    serde_json::to_value(
        session
            .port()
            .request(serde_json::from_value(intent(id)).unwrap())
            .unwrap()
            .recv_timeout(std::time::Duration::from_secs(3))
            .unwrap(),
    )
    .unwrap()
}
fn join(session: &NativeSession) -> NativeOuterJoined {
    loop {
        if let Some(joined) = session.owner().poll_join() {
            return joined;
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
}
#[cfg(resident_launch_fixture)]
#[path = "session_close_checks.rs"]
mod close;
pub(crate) fn run(mode: &str) {
    #[cfg(resident_launch_fixture)]
    if matches!(mode, "session-close-pending" | "session-close-idle") {
        close::run(mode);
        return;
    }
    let mut passed = 0;
    let mut check = |v: bool| {
        assert!(v, "session condition {}", passed + 1);
        passed += 1;
    };
    if mode == "session-normal" {
        let session = NativeSession::prepare().unwrap();
        check(status(&session)["state"] == "source-unavailable");
        check(receipt(&session, 1)["disposition"] == "unavailable");
        check(!session.owner().request_exit());
        let joined = join(&session);
        check(joined.native().outcome() == NativeOutcome::Refused);
        check(!joined.wire_close_confirmed());
        check(session.owner().outcome() == Some(Outcome::Unknown));
        check(!session.owner().finish_requested());
        check(status(&session)["state"] == "source-unavailable");
        drop(session);
        report(mode, passed, false);
        return;
    }
    #[cfg(resident_launch_fixture)]
    active(mode);
    #[cfg(not(resident_launch_fixture))]
    panic!("active session fixture unavailable");
}
#[cfg(resident_launch_fixture)]
fn active(mode: &str) {
    let mut passed = 0;
    let mut check = |v: bool| {
        assert!(v, "session condition {}", passed + 1);
        passed += 1;
    };
    let (fixture, id, unavailable) = match mode {
        "session-confirmed" => (FixedActiveFixture::Echo, 1, false),
        "session-unknown" => (FixedActiveFixture::Echo, 2, false),
        "session-missing" => (FixedActiveFixture::Echo, 3, false),
        "session-malformed" => (FixedActiveFixture::Echo, 4, false),
        "session-lost" => (FixedActiveFixture::LostCloseReceipt, 1, true),
        "session-invalid" => (FixedActiveFixture::Early, 1, true),
        "session-rejected" => (FixedActiveFixture::ExitTwo, 1, true),
        "session-unwind" => (FixedActiveFixture::Echo, 1, true),
        "session-drop" => (FixedActiveFixture::Echo, 1, false),
        _ => panic!("unknown native session fixture"),
    };
    let session = NativeSession::fixture(fixture, mode == "session-unwind").unwrap();
    let before = status(&session);
    check(
        before["state"]
            == if unavailable {
                "source-unavailable"
            } else {
                "source-observation"
            },
    );
    check(!session.owner().finish_requested());
    check(session.owner().poll_join().is_none() || unavailable);
    if unavailable {
        check(receipt(&session, id)["disposition"] == "unavailable");
    } else {
        // A borrowed viewer disappears; this does not issue close or OFF.
        {
            let view = session.port();
            check(serde_json::to_value(view.current()).unwrap()["standby"] == "off");
        }
        check(NativePreparation::fixture_active_stats().waits == 0);
        // Dropping a borrowed view changes no observation fields, but the
        // original freshness deadline continues to elapse. Cross a real clock
        // tick so comparing the entire JSON cannot accidentally pass.
        std::thread::sleep(std::time::Duration::from_millis(5));
        let after = status(&session);
        let before_budget = before["validForMs"].as_u64().unwrap();
        let after_budget = after["validForMs"].as_u64().unwrap();
        let mut stable_before = before.clone();
        let mut stable_after = after;
        stable_before.as_object_mut().unwrap().remove("validForMs");
        stable_after.as_object_mut().unwrap().remove("validForMs");
        check(stable_after == stable_before && after_budget > 0 && after_budget < before_budget);
        check(
            session
                .port()
                .request(serde_json::from_value(intent(0)).unwrap())
                .is_err(),
        );
        let mut injection = intent(id);
        injection["path"] = json!("synthetic-invalid");
        check(serde_json::from_value::<super::model::IntentRequest>(injection).is_err());
        check(receipt(&session, id)["disposition"] == "accepted");
        // Acceptance alone cannot fabricate an applied ON observation.
        check(status(&session)["standby"] == "off");
        check(status(&session)["revision"] == 1);
        check(NativePreparation::fixture_active_stats().waits == 0);
    }
    if mode != "session-drop" {
        check(!session.owner().request_exit());
        let joined = join(&session);
        check(matches!(
            joined.native().outcome(),
            NativeOutcome::Active { .. }
        ));
        check(joined.wire_close_confirmed() == matches!(mode, "session-confirmed"));
        check(session.owner().outcome() == Some(Outcome::Unknown));
        check(!session.owner().finish_requested());
        check(session.owner().poll_join() == Some(joined));
        check(
            status(&session)["state"] == "source-unavailable"
                || status(&session)["validForMs"] == 0,
        );
        check(
            receipt(&session, id)["disposition"]
                == if unavailable {
                    "unavailable"
                } else {
                    "rejected"
                },
        );
    }
    drop(session); // Original outer guard closes/joins; never detached on failure.
    check(NativePreparation::fixture_active_stats().waits == 1);
    check(NativePreparation::fixture_counts().open == 0);
    check(NativePreparation::fixture_counts().closes == 4);
    check(NativePreparation::fixture_retained_state() == 3);
    // Actual native record remains occupied after all visible component joins.
    check(matches!(
        NativePreparation::fixture_active(FixedActiveFixture::Echo),
        Preparation::Refused
    ));
    check(NativePreparation::fixture_active_stats().waits == 1);
    report(mode, passed, true);
}
fn report(mode: &str, passed: usize, child: bool) {
    let child = u8::from(child);
    println!(
        "NATIVE_SESSION_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted={child} childrenWaited={child} open=0 nativeUnknown={child} producer=0"
    );
}

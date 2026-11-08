//! End-to-end native close admission; assert negatives only after owner cleanup.
use super::{join, report, status, NativeSession};
use crate::native_preparation::{
    ExitObservation, FixedActiveFixture, NativePreparation, Preparation, State,
};
use crate::resident_port::duplex::owner::native::NativeOutcome;
use crate::resident_port::model::Receipt;
use crate::standby_lifecycle::Outcome;
use serde_json::{json, Value};
use std::sync::mpsc::{Receiver, TryRecvError};
use std::time::{Duration, Instant};
fn submit(session: &NativeSession, id: u64, revision: u64) -> Receiver<Receipt> {
    session
        .port()
        .request(
            serde_json::from_value(json!({"protocolVersion":1,
        "requestId":id,"expectedRevision":revision,"intent":"on"}))
            .unwrap(),
        )
        .unwrap()
}
fn receive(response: &Receiver<Receipt>) -> Value {
    response
        .recv_timeout(Duration::from_secs(3))
        .ok()
        .map(|r| serde_json::to_value(r).unwrap())
        .unwrap_or(Value::Null)
}
pub(super) fn run(mode: &str) {
    let session = NativeSession::fixture(FixedActiveFixture::Echo, false).unwrap();
    let mut checks = vec![("initial-off", status(&session)["standby"] == "off")];
    let pending = if mode == "session-close-pending" {
        let response = submit(&session, 5, 1);
        let start = Instant::now();
        while status(&session)["revision"] != 2 {
            assert!(
                start.elapsed() < Duration::from_secs(3),
                "pending child observation"
            );
            std::thread::sleep(Duration::from_millis(1));
        }
        let observed = status(&session);
        checks.push(("written-not-applied", observed["standby"] == "off"));
        checks.push((
            "pending-correlated",
            observed["transition"] == json!({"requestId":5,"intent":"on","outcome":"pending"}),
        ));
        checks.push((
            "withheld-until-eof",
            matches!(response.try_recv(), Err(TryRecvError::Empty)),
        ));
        Some(response)
    } else {
        None
    };
    checks.push((
        "wait-not-yet",
        NativePreparation::fixture_active_stats().waits == 0,
    ));
    checks.push(("quit-denied", !session.owner().request_exit()));
    let first = pending.as_ref().map(receive);
    if let Some(receipt) = &first {
        checks.push((
            "pending-close-is-unknown",
            receipt["disposition"] == "unknown",
        ));
        checks.push(("pending-original-id", receipt["requestId"] == 5));
    }
    let revision = if pending.is_some() { 2 } else { 1 };
    let after_close = receive(&submit(&session, 6, revision));
    checks.push((
        "closed-submission-rejected",
        after_close["disposition"] == "rejected",
    ));
    checks.push(("closed-original-id", after_close["requestId"] == 6));
    let snapshot = status(&session);
    checks.push(("closed-status-expired", snapshot["validForMs"] == 0));
    checks.push(("closed-no-on-admission", snapshot["canRequestOn"] == false));
    checks.push((
        "closed-no-off-admission",
        snapshot["canRequestOff"] == false,
    ));
    let joined = join(&session); // original exact Wait / IO worker / outer joins
    checks.push((
        "same-exit-and-io",
        joined.native().outcome()
            == NativeOutcome::Active {
                exit: ExitObservation::Exit(0),
                io: State::ActiveIoRetired,
                unwound: false,
            },
    ));
    checks.push(("wire-component-only", joined.wire_close_confirmed()));
    checks.push((
        "native-unknown",
        session.owner().outcome() == Some(Outcome::Unknown),
    ));
    checks.push(("no-quit-after-join", !session.owner().finish_requested()));
    checks.push((
        "same-outer-proof",
        session.owner().poll_join() == Some(joined),
    ));
    checks.push((
        "after-join-still-rejected",
        receive(&submit(&session, 7, revision))["disposition"] == "rejected",
    ));
    checks.push(("not-promoted-on", status(&session)["standby"] == "off"));
    if let Some(response) = pending {
        checks.push((
            "late-acceptance-not-delivered",
            matches!(response.try_recv(), Err(TryRecvError::Disconnected)),
        ));
        checks.push((
            "late-acceptance-not-applied",
            status(&session)["transition"]["outcome"] == "pending",
        ));
    }
    drop(session); // even deliberate mutant assertions occur only AFTER this
    checks.push((
        "original-wait-once",
        NativePreparation::fixture_active_stats().waits == 1,
    ));
    checks.push((
        "all-endpoints-closed",
        NativePreparation::fixture_counts().open == 0,
    ));
    checks.push((
        "close-once",
        NativePreparation::fixture_counts().closes == 4,
    ));
    checks.push((
        "unknown-record-retained",
        NativePreparation::fixture_retained_state() == 3,
    ));
    checks.push((
        "no-replacement",
        matches!(
            NativePreparation::fixture_active(FixedActiveFixture::Echo),
            Preparation::Refused
        ),
    ));
    checks.push((
        "no-replacement-wait",
        NativePreparation::fixture_active_stats().waits == 1,
    ));
    eprintln!(
        "NATIVE_CLOSE_AUDIT mode={mode} first={first:?} after={after_close} waits={} open={}",
        NativePreparation::fixture_active_stats().waits,
        NativePreparation::fixture_counts().open
    );
    for (name, ok) in &checks {
        assert!(*ok, "native close condition: {name}");
    }
    report(mode, checks.len(), true);
}

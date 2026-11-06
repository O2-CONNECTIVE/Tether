use super::super::model::Disposition;
use super::test_frames::{intent_request as request, status_line as status};
use super::{
    tracking::Tracking,
    wire::{self, Event},
};
use serde_json::json;
fn feed(tracking: &mut Tracking, line: &str, now: u64) -> Result<(), ()> {
    tracking
        .event(Event::parse(line.as_bytes())?, now)
        .map(|_| ())
}
fn transition(id: u64, outcome: &str) -> serde_json::Value {
    json!({"requestId":id,"intent":"on","outcome":outcome})
}
#[test]
fn strict_wire_rejects_duplicate_unknown_fields_and_bad_numbers() {
    let valid = status(1, 100, json!(null));
    assert!(Event::parse(valid.as_bytes()).is_ok());
    for bad in [
        valid.replacen(
            "\"type\":\"status\"",
            "\"type\":\"status\",\"type\":\"status\"",
            1,
        ),
        valid.replacen("\"revision\":1", "\"revision\":1,\"revision\":1", 1),
        valid.replacen(
            "\"protocolVersion\":1",
            "\"protocolVersion\":1,\"unexpected\":true",
            1,
        ),
        valid.replace("\"revision\":1", "\"revision\":1.0"),
        valid.replace(
            "\"sourceContinuousMs\":100",
            "\"sourceContinuousMs\":9007199254740992",
        ),
        valid.replace("\"caller\":null,", ""),
        valid.replace("\"transition\":null,", ""),
        valid.replace("\"caller\":null", "\"caller\":null,\"caller\":null"),
        valid.replace(
            "\"state\":\"source-observation\"",
            "\"state\":\"source-unavailable\"",
        ),
    ] {
        assert!(Event::parse(bad.as_bytes()).is_err(), "accepted {bad}");
    }
    assert!(Event::parse(&vec![b' '; wire::MAX_LINE + 1]).is_err());
}
#[test]
fn freshness_does_not_renew_same_revision_and_new_heartbeat_can_advance() {
    let mut t = Tracking::default();
    feed(&mut t, &status(1, 100, json!(null)), 100).unwrap();
    assert_eq!(t.current(600).unwrap().valid_for_ms, 2500);
    feed(&mut t, &status(2, 600, json!(null)), 600).unwrap();
    assert_eq!(t.current(600).unwrap().valid_for_ms, 3000);
    assert!(feed(&mut t, &status(2, 700, json!(null)), 700).is_err());
    assert_eq!(t.current(700).unwrap().valid_for_ms, 0);
}
#[test]
fn stale_future_clock_rollback_and_bad_invariants_fail_closed() {
    for (line, now) in [
        (status(1, 100, json!(null)), 99),
        (status(1, 100, json!(null)), 3101),
        (status(0, 100, json!(null)), 100),
        (
            status(1, 100, json!(null))
                .replace("\"cleanup\":\"confirmed\"", "\"cleanup\":\"unknown\""),
            100,
        ),
    ] {
        let mut t = Tracking::default();
        assert!(feed(&mut t, &line, now).is_err());
        assert!(t.failed);
    }
    let mut t = Tracking::default();
    feed(&mut t, &status(1, 100, json!(null)), 100).unwrap();
    assert_eq!(t.current(99).unwrap().valid_for_ms, 0);
}
#[test]
fn unresolved_identity_survives_null_and_unrelated_terminal_status() {
    let mut t = Tracking::default();
    feed(&mut t, &status(1, 100, transition(9, "pending")), 100).unwrap();
    for (revision, transition) in [(2, json!(null)), (3, transition(10, "rejected"))] {
        feed(&mut t, &status(revision, 100, transition), 100).unwrap();
        assert_eq!(
            t.begin(&request(11 + revision, revision), 100),
            Err(Disposition::Rejected)
        );
    }
    feed(&mut t, &status(4, 100, transition(9, "rejected")), 100).unwrap();
    assert!(t.begin(&request(20, 4), 100).is_ok());
}
#[test]
fn second_unresolved_pair_poison_is_permanent() {
    for next in [
        transition(10, "pending"),
        json!({"requestId":9,"intent":"off","outcome":"pending"}),
    ] {
        let mut t = Tracking::default();
        feed(&mut t, &status(1, 100, transition(9, "pending")), 100).unwrap();
        assert!(feed(&mut t, &status(2, 100, next), 100).is_err());
        assert!(feed(&mut t, &status(3, 100, transition(9, "rejected")), 100).is_err());
    }
}
#[test]
fn receipt_is_not_transition_completion_and_must_match_once() {
    let mut t = Tracking::default();
    feed(&mut t, &status(1, 100, json!(null)), 100).unwrap();
    t.begin(&request(1, 1), 100).unwrap();
    let receipt = br#"{"protocolVersion":1,"type":"receipt","receipt":{"protocolVersion":1,"requestId":1,"intent":"on","disposition":"accepted"}}"#;
    assert!(t
        .event(Event::parse(receipt).unwrap(), 100)
        .unwrap()
        .is_some());
    assert_eq!(t.begin(&request(2, 1), 100), Err(Disposition::Rejected));
    assert!(t.event(Event::parse(receipt).unwrap(), 100).is_err());
}
#[test]
fn authoritative_transition_may_arrive_before_matching_receipt() {
    let mut t = Tracking::default();
    feed(&mut t, &status(1, 100, json!(null)), 100).unwrap();
    t.begin(&request(1, 1), 100).unwrap();
    feed(&mut t, &status(2, 100, transition(1, "rejected")), 100).unwrap();
    let receipt = br#"{"protocolVersion":1,"type":"receipt","receipt":{"protocolVersion":1,"requestId":1,"intent":"on","disposition":"rejected"}}"#;
    assert!(t
        .event(Event::parse(receipt).unwrap(), 100)
        .unwrap()
        .is_some());
}

#[test]
fn observed_transition_ids_are_lifetime_highwater() {
    let mut t = Tracking::default();
    feed(&mut t, &status(1, 100, transition(9, "pending")), 100).unwrap();
    feed(&mut t, &status(2, 100, transition(9, "rejected")), 100).unwrap();
    assert_eq!(t.begin(&request(8, 2), 100), Err(Disposition::Stale));
    assert_eq!(t.begin(&request(9, 2), 100), Err(Disposition::Stale));
    assert!(t.begin(&request(10, 2), 100).is_ok());
}

#[test]
fn applied_on_is_historical_admission_not_current_connection() {
    // The reviewed concrete owner/producer establishes prior real auth. These
    // decoded fields alone are not provenance; production remains unbound.
    let line = status(1, 100, transition(1, "applied"))
        .replace("\"standby\":\"off\"", "\"standby\":\"on\"")
        .replace("\"canRequestOn\":true", "\"canRequestOn\":false");
    for registration in ["authenticated", "connecting", "disconnected"] {
        let mut t = Tracking::default();
        feed(&mut t, &line.replace("disconnected", registration), 100).unwrap();
        assert_eq!(
            serde_json::to_value(t.current(100).unwrap()).unwrap()["registration"],
            registration
        );
    }
    let mut t = Tracking::default();
    assert!(feed(&mut t, &line.replace("disconnected", "unknown"), 100).is_err());
}

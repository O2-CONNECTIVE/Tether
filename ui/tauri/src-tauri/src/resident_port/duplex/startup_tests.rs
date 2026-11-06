use super::*;
use crate::resident_port::duplex::{
    exchange::Shared,
    test_frames::{intent_request as request, status_line as status},
    wire::Event,
};
use serde_json::json;
fn rejected(at: u64) -> Event {
    Event::parse(format!(r#"{{"protocolVersion":1,"type":"startupRejected","sourceContinuousMs":{at},"code":"policy-rejected"}}"#).as_bytes()).unwrap()
}
#[test]
fn startup_rejection_clock_boundary_and_rollback_are_sticky() {
    for (at, now) in [(101, 100), (100, 3100)] {
        let mut s = Startup::default();
        assert!(s.event(&rejected(at), now).is_err());
        s.eof();
        assert!(!s.confirmed());
    }
    let mut s = Startup::default();
    assert!(s.event(&rejected(100), 3099).unwrap());
    assert!(!s.confirmed());
    assert!(s.tick(3098).is_err());
    s.eof();
    assert!(!s.confirmed());
    let mut s = Startup::default();
    s.tick(200).unwrap();
    assert!(s.event(&rejected(150), 150).is_err());
}
#[test]
fn startup_rejection_requires_no_prior_frame_or_intent_even_unsent() {
    let mut s = Startup::default();
    s.event(
        &Event::parse(status(1, 100, json!(null)).as_bytes()).unwrap(),
        100,
    )
    .unwrap();
    assert!(s.event(&rejected(100), 100).is_err());
    let mut s = Shared::default();
    let _ = s.submit(request(1, 1), 100);
    assert!(s.output.is_none());
    assert!(s.startup.event(&rejected(100), 100).is_err());
    let mut s = Startup::default();
    s.event(&rejected(100), 100).unwrap();
    s.eof();
    assert!(s.confirmed());
    assert!(s.event(&rejected(100), 100).is_err());
    assert!(!s.confirmed());
}
#[test]
fn startup_rejection_wire_rejects_extra_duplicate_wrong_types_and_codes() {
    let valid = r#"{"protocolVersion":1,"type":"startupRejected","sourceContinuousMs":100,"code":"policy-rejected"}"#;
    for invalid in [
        valid.replace("100", "100.0"),
        valid.replace("100", "9007199254740992"),
        valid.replace("100", "-1"),
        valid.replace("policy-rejected", "unknown"),
        valid.replace(r#""code":"#, r#""extra":0,"code":"#),
        valid.replace(
            r#""protocolVersion":1"#,
            r#""protocolVersion":1,"protocolVersion":1"#,
        ),
        valid.replace(
            r#""code":"policy-rejected""#,
            r#""code":"policy-rejected","code":"policy-rejected""#,
        ),
        valid.replace(
            r#""type":"startupRejected""#,
            r#""type":"startupRejected","type":"startupRejected""#,
        ),
        valid.replace(
            r#""sourceContinuousMs":100"#,
            r#""sourceContinuousMs":100,"sourceContinuousMs":100"#,
        ),
    ] {
        assert!(Event::parse(invalid.as_bytes()).is_err(), "{invalid}");
    }
}

#[test]
fn startup_rejection_later_transport_or_clock_failure_invalidates_before_eof() {
    let mut s = Shared::default();
    assert!(s.startup.event(&rejected(100), 100).unwrap());
    s.fault();
    s.ended();
    assert!(!s.startup.confirmed());
    let mut s = Shared::default();
    s.sample(200).unwrap();
    assert!(s.startup.event(&rejected(200), 200).unwrap());
    assert!(s.sample(199).is_err());
    s.ended();
    assert!(!s.startup.confirmed());
}

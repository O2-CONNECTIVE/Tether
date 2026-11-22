use super::{test_frames, wire::Event};
use crate::resident_port::model::SAFE_INTEGER;
use serde_json::{json, Value};

fn accepted(value: Value) -> bool {
    let line = value.to_string();
    let Ok(Event::Status { snapshot, .. }) = Event::parse(line.as_bytes()) else {
        return false;
    };
    snapshot.observation().is_ok()
}

#[test]
fn status_requires_the_three_standby_end_fields() {
    let valid = test_frames::status(100);
    assert!(accepted(valid.clone()));
    for key in ["standbyUntil", "standbyEndingSoon", "standbyExpired"] {
        let mut missing = valid.clone();
        missing["snapshot"].as_object_mut().unwrap().remove(key);
        assert!(!accepted(missing), "accepted missing {key}");
    }
    let mut extra = valid;
    extra["snapshot"]["unexpected"] = json!(true);
    assert!(!accepted(extra));
}

#[test]
fn status_checks_standby_end_types_and_safe_integer_bounds() {
    let mut maximum = test_frames::status(100);
    maximum["snapshot"]["standbyUntil"] = json!(SAFE_INTEGER);
    maximum["snapshot"]["standbyEndingSoon"] = json!(true);
    assert!(accepted(maximum));

    for until in [
        json!(0),
        json!(-1),
        json!(1.5),
        json!(SAFE_INTEGER + 1),
        json!("1900000000000"),
    ] {
        let mut invalid = test_frames::status(100);
        invalid["snapshot"]["standbyUntil"] = until.clone();
        assert!(!accepted(invalid), "accepted standbyUntil {until}");
    }

    let mut warning_without_end = test_frames::status(100);
    warning_without_end["snapshot"]["standbyEndingSoon"] = json!(true);
    assert!(!accepted(warning_without_end));

    for (key, value) in [
        ("standbyEndingSoon", json!("false")),
        ("standbyExpired", json!("false")),
    ] {
        let mut invalid = test_frames::status(100);
        invalid["snapshot"][key] = value;
        assert!(!accepted(invalid), "accepted invalid {key}");
    }
}

#[test]
fn expired_reason_survives_unknown_cleanup_status() {
    let mut poisoned = test_frames::status(100);
    poisoned["snapshot"]["validForMs"] = json!(0);
    poisoned["snapshot"]["standby"] = json!("unknown");
    poisoned["snapshot"]["cleanup"] = json!("unknown");
    poisoned["snapshot"]["canRequestOn"] = json!(false);
    poisoned["snapshot"]["standbyExpired"] = json!(true);
    assert!(accepted(poisoned));
}

#[test]
fn transition_rejects_extend_intent() {
    let mut extended = test_frames::status(100);
    extended["snapshot"]["transition"] =
        json!({"requestId":1,"intent":"extend","outcome":"applied"});
    assert!(!accepted(extended));
}

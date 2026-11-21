use super::{test_frames::*, wire::Event};
use crate::resident_port::model::SAFE_INTEGER;
use serde_json::json;

#[test]
fn accepts_only_versioned_resident_event_shapes() {
    assert!(matches!(
        event(&terminal(100, "confirmed")),
        Event::Closed { .. }
    ));
    assert!(matches!(event(&receipt()), Event::Receipt { .. }));
    assert!(matches!(event(&rejection()), Event::StartupRejected { .. }));
    let Event::Status { snapshot, .. } = event(&status(100)) else {
        panic!("wrong event");
    };
    assert!(snapshot.observation().is_ok());
}

#[test]
fn rejects_unknown_duplicate_missing_and_wrong_version_fields() {
    let mut values = Vec::new();
    for field in ["protocolVersion", "sourceContinuousMs", "outcome"] {
        let mut value = terminal(100, "confirmed");
        value.as_object_mut().unwrap().remove(field);
        values.push(value);
    }
    for version in [json!(0), json!(2), json!("1"), json!(1.0)] {
        let mut value = terminal(100, "confirmed");
        value["protocolVersion"] = version;
        values.push(value);
    }
    let mut unknown = terminal(100, "confirmed");
    unknown["extra"] = json!(true);
    values.push(unknown);
    for value in values {
        assert!(Event::parse(&serde_json::to_vec(&value).unwrap()).is_err());
    }
    assert!(Event::parse(br#"{"protocolVersion":1,"type":"closed","sourceContinuousMs":100,"outcome":"confirmed","outcome":"unknown"}"#).is_err());
    assert!(Event::parse(br#"{"protocolVersion":1,"protocolVersion":1,"type":"closed","sourceContinuousMs":100,"outcome":"confirmed"}"#).is_err());
}

#[test]
fn payload_length_and_carriage_return_bounds_are_exact() {
    let bytes = serde_json::to_vec(&terminal(100, "confirmed")).unwrap();
    let mut maximum = vec![b' '; super::wire::MAX_LINE - bytes.len()];
    maximum.extend_from_slice(&bytes);
    assert!(Event::parse(&maximum).is_ok());
    maximum.push(b' ');
    assert!(Event::parse(&maximum).is_err());
    for bytes in [b"".as_slice(), b"\r{}", b"\xef\xbb\xbf{}", b"[]", b"{}{}"] {
        assert!(Event::parse(bytes).is_err());
    }
}

#[test]
fn safe_integer_clock_and_receipt_ids_are_not_coerced() {
    assert!(
        Event::parse(&serde_json::to_vec(&terminal(SAFE_INTEGER, "confirmed")).unwrap()).is_ok()
    );
    assert!(
        Event::parse(&serde_json::to_vec(&terminal(SAFE_INTEGER + 1, "confirmed")).unwrap())
            .is_err()
    );
    for field in [
        "requestId",
        "protocolVersion",
        "intent",
        "disposition",
        "extra",
    ] {
        let mut value = receipt();
        value["receipt"][field] = match field {
            "requestId" => json!(0),
            "protocolVersion" => json!(2),
            "intent" => json!("start"),
            "disposition" => json!("done"),
            _ => json!(true),
        };
        assert!(Event::parse(&serde_json::to_vec(&value).unwrap()).is_err());
    }
    let mut value = receipt();
    value["receipt"]["requestId"] = json!(SAFE_INTEGER + 1);
    assert!(Event::parse(&serde_json::to_vec(&value).unwrap()).is_err());
    value["receipt"]["requestId"] = json!(SAFE_INTEGER);
    assert!(Event::parse(&serde_json::to_vec(&value).unwrap()).is_ok());
}

#[test]
fn nullable_and_unknown_snapshot_fields_are_strict() {
    for field in ["caller", "transition"] {
        let mut value = status(100);
        value["snapshot"].as_object_mut().unwrap().remove(field);
        assert!(Event::parse(&serde_json::to_vec(&value).unwrap()).is_err());
    }
    let mut value = status(100);
    value["snapshot"]["extra"] = json!(true);
    assert!(Event::parse(&serde_json::to_vec(&value).unwrap()).is_err());
}

#[test]
fn accepts_reconnecting_and_needs_action_states_with_registration_reasons() {
    for (registration, reason) in [
        ("reconnecting", None),
        ("needs_action", Some("identity_refused")),
        ("needs_action", Some("connection_occupied")),
        ("needs_action", Some("gateway_refused")),
    ] {
        let mut value = status(100);
        value["snapshot"]["standby"] = json!("on");
        value["snapshot"]["registration"] = json!(registration);
        value["snapshot"]["work"] = json!("idle");
        value["snapshot"]["cleanup"] = json!("confirmed");
        value["snapshot"]["canRequestOn"] = json!(false);
        value["snapshot"]["canRequestOff"] = json!(true);
        if let Some(reason) = reason {
            value["snapshot"]["registrationReason"] = json!(reason);
        }
        let Event::Status { snapshot, .. } = event(&value) else {
            panic!("wrong event");
        };
        let observation = snapshot.observation().expect("valid registration state");
        let encoded = serde_json::to_value(observation).unwrap();
        assert_eq!(encoded["registration"], json!(registration));
        assert_eq!(
            encoded.get("registrationReason").and_then(|v| v.as_str()),
            reason
        );
    }
}

#[test]
fn needs_action_requires_a_known_registration_reason() {
    for reason in [None, Some("unknown_reason")] {
        let mut value = status(100);
        value["snapshot"]["standby"] = json!("on");
        value["snapshot"]["registration"] = json!("needs_action");
        value["snapshot"]["work"] = json!("idle");
        value["snapshot"]["cleanup"] = json!("confirmed");
        value["snapshot"]["canRequestOn"] = json!(false);
        value["snapshot"]["canRequestOff"] = json!(true);
        if let Some(reason) = reason {
            value["snapshot"]["registrationReason"] = json!(reason);
        }
        let bytes = serde_json::to_vec(&value).unwrap();
        match Event::parse(&bytes) {
            Ok(Event::Status { snapshot, .. }) => {
                assert!(
                    snapshot.observation().is_err(),
                    "accepted reason {reason:?}"
                );
            }
            Err(()) if reason == Some("unknown_reason") => {}
            result => panic!(
                "unexpected parse result for reason {reason:?}: {}",
                result.is_ok()
            ),
        }
    }
}

#[test]
fn observation_invariants_are_validated_before_close_accepts_status() {
    let cases = [
        ("revision", json!(0)),
        ("revision", json!(SAFE_INTEGER + 1)),
        ("validForMs", json!(3001)),
        ("validForMs", json!(0)),
        ("caller", json!("")),
        ("caller", json!("line\nbreak")),
        ("caller", json!("a".repeat(81))),
        ("canRequestOff", json!(true)),
        ("cleanup", json!("unknown")),
        (
            "transition",
            json!({"requestId":0,"intent":"off","outcome":"pending"}),
        ),
        (
            "transition",
            json!({"requestId":1,"intent":"on","outcome":"applied"}),
        ),
    ];
    for (field, invalid) in cases {
        let mut value = status(100);
        value["snapshot"][field] = invalid;
        let Event::Status { snapshot, .. } = event(&value) else {
            panic!("wrong event")
        };
        assert!(snapshot.observation().is_err(), "accepted {field}");
    }
    let mut value = status(100);
    value["snapshot"]["standby"] = json!("on");
    value["snapshot"]["canRequestOn"] = json!(false);
    value["snapshot"]["registration"] = json!("unknown");
    value["snapshot"]["transition"] = json!({"requestId":1,"intent":"on","outcome":"applied"});
    let Event::Status { snapshot, .. } = event(&value) else {
        panic!("wrong event")
    };
    assert!(snapshot.observation().is_err());
}

use super::{
    exchange::Shared,
    test_frames,
    wire::{self, Event},
};
use crate::resident_port::model::{Disposition, Intent, IntentRequest};
use serde_json::{json, Value};

fn request(intent: &str, id: u64, hours: Option<Value>) -> Value {
    let mut value = json!({"protocolVersion":1,"requestId":id,
        "expectedRevision":1,"intent":intent});
    if let Some(hours) = hours {
        value["standbyHours"] = hours;
    }
    value
}

fn valid_request(value: Value) -> bool {
    match serde_json::from_value::<IntentRequest>(value) {
        Ok(request) => request.validate().is_ok(),
        Err(_) => false,
    }
}

#[test]
fn standby_hours_follow_intent_rules_and_serialize_without_defaulting() {
    assert!(valid_request(request("on", 1, None)));
    assert!(valid_request(request("off", 1, None)));
    for hours in [2, 4, 8, 12, 24] {
        assert!(valid_request(request("on", 1, Some(json!(hours)))));
        assert!(valid_request(request("extend", 1, Some(json!(hours)))));
    }
    assert!(!valid_request(request("extend", 1, None)));
    assert!(!valid_request(request("off", 1, Some(json!(2)))));
    assert!(!valid_request(request("allow-run", 1, None)));
    assert!(!valid_request(request("deny-run", 1, None)));
    for invalid in [json!(3), json!(0), json!(4.0), json!("4"), json!(null)] {
        assert!(!valid_request(request("on", 1, Some(invalid.clone()))));
        assert!(!valid_request(request("extend", 1, Some(invalid))));
    }

    let on: IntentRequest = serde_json::from_value(request("on", 1, None)).unwrap();
    let frame: Value = serde_json::from_slice(&wire::encode(&on).unwrap()).unwrap();
    assert!(frame["request"].get("standbyHours").is_none());
    let extend: IntentRequest =
        serde_json::from_value(request("extend", 2, Some(json!(8)))).unwrap();
    let frame: Value = serde_json::from_slice(&wire::encode(&extend).unwrap()).unwrap();
    assert_eq!(frame["request"]["standbyHours"], json!(8));
}

#[test]
fn extend_waits_only_for_receipt_even_when_can_flags_are_false() {
    let mut status = test_frames::status(100);
    status["snapshot"]["canRequestOn"] = json!(false);
    status["snapshot"]["canRequestOff"] = json!(false);
    let mut shared = Shared::default();
    shared
        .tracking
        .event(test_frames::event(&status), 100)
        .unwrap();

    let first: IntentRequest =
        serde_json::from_value(request("extend", 1, Some(json!(4)))).unwrap();
    let first_receipt = shared.submit(first, 100);
    let output = shared.output.as_ref().unwrap();
    let frame: Value = serde_json::from_slice(output).unwrap();
    assert_eq!(frame["request"]["intent"], json!("extend"));
    assert_eq!(frame["request"]["standbyHours"], json!(4));
    let output_len = output.len();
    shared.written(Ok(output_len)).unwrap();
    assert_eq!(shared.tracking.pending, None);
    assert_eq!(shared.tracking.awaiting, Some((1, Intent::Extend)));

    let concurrent: IntentRequest =
        serde_json::from_value(request("extend", 2, Some(json!(8)))).unwrap();
    let rejected = shared.submit(concurrent, 100).try_recv().unwrap();
    assert_eq!(rejected.disposition, Disposition::Rejected);
    let receipt = json!({"protocolVersion":1,"type":"receipt","receipt":{
        "protocolVersion":1,"requestId":1,"intent":"extend","disposition":"accepted"}});
    let receipt = Event::parse(&serde_json::to_vec(&receipt).unwrap()).unwrap();
    let receipt = shared.tracking.event(receipt, 100).unwrap().unwrap();
    let (_, send, _) = shared.waiting.take().unwrap();
    assert!(send.try_send(receipt).is_ok());
    assert_eq!(first_receipt.recv().unwrap().disposition, Disposition::Accepted);
    assert_eq!(shared.tracking.awaiting, None);

    let next: IntentRequest =
        serde_json::from_value(request("extend", 3, Some(json!(12)))).unwrap();
    let _next_receipt = shared.submit(next, 100);
    assert!(shared.output.is_some());
}

use super::*;
use serde_json::{json, Value};

#[test]
fn fixed_stage_publication_preserves_partial_and_finished_windows() {
    for stages in [
        vec![Stage::SetupEnter, Stage::BuildStart],
        vec![
            Stage::SetupEnter,
            Stage::BuildStart,
            Stage::PageFinished,
            Stage::BuildOk,
            Stage::SetupReturn,
            Stage::PageFinished,
        ],
    ] {
        let memory = Recorder::memory();
        let trace = Trace(Some(memory.clone()));
        for stage in stages {
            trace.record(stage);
        }
        assert_eq!(memory.complete(), memory.page_seen.load(SeqCst));
        let text = memory.snapshot(true);
        assert!(text.len() <= 2048);
        let value: Value = serde_json::from_str(&text).unwrap();
        let events = value["events"].as_array().unwrap();
        assert!(events.len() == 2 || events.len() == 5);
        assert_eq!(value["attempted"], json!(events.len()));
        assert_eq!(value["recorded"], json!(events.len()));
        assert_eq!(value["dropped"], json!(0));
        assert_eq!(value["sealed"], json!(true));
        assert_eq!(value["inFlight"], json!(0));
        assert_eq!(events[0]["stage"], json!("setup-enter"));
        for (index, event) in events.iter().enumerate() {
            assert_eq!(event["seq"], json!(index + 1));
            assert_eq!(event.as_object().unwrap().len(), 3);
        }
        trace.record(Stage::BuildError);
        assert_eq!(memory.attempted.load(SeqCst), events.len() as u64);
    }
}

#[test]
fn contention_and_capacity_are_non_waiting_and_report_missing_records() {
    let memory = Recorder::memory();
    let trace = Trace(Some(memory.clone()));
    let guard = memory.events.lock().unwrap();
    trace.record(Stage::SetupEnter);
    drop(guard);
    for _ in 0..17 {
        trace.record(Stage::BuildStart);
    }
    let value: Value = serde_json::from_str(&memory.snapshot(true)).unwrap();
    assert_eq!(value["attempted"], json!(18));
    assert_eq!(value["recorded"], json!(16));
    assert_eq!(value["dropped"], json!(2));
}

#[test]
fn missing_finish_marker_cannot_seal_the_observation_window() {
    let memory = Recorder::memory();
    Trace(Some(memory.clone())).record(Stage::SetupEnter);
    let value: Value = serde_json::from_str(&memory.snapshot(false)).unwrap();
    assert_eq!(value["sealed"], json!(false));
    assert_eq!(value["events"][0]["stage"], json!("setup-enter"));
}

#[test]
fn initialization_stages_are_bounded_hints_not_startup_completion() {
    let memory = Recorder::memory();
    memory.wry_hook.store(true, SeqCst);
    let trace = Trace(Some(memory.clone()));
    for stage in [
        Stage::SetupEnter,
        Stage::BuildStart,
        Stage::EnvironmentRequest,
        Stage::EnvironmentCallbackOk,
        Stage::EnvironmentWaitOk,
        Stage::ControllerRequest,
        Stage::ControllerCallbackOk,
        Stage::ControllerWaitOk,
    ] {
        trace.record(stage);
    }
    assert!(!memory.complete());
    let text = memory.snapshot(true);
    assert!(text.len() <= 2048);
    let value: Value = serde_json::from_str(&text).unwrap();
    assert_eq!(value["variant"], json!("host-startup-stage-2"));
    assert_eq!(value["wryHook"], json!(true));
    assert_eq!(value["sealReason"], json!("requested"));
    assert_eq!(value["events"].as_array().unwrap().len(), 8);
    let last = value["events"][7]["elapsedUs"].as_u64().unwrap();
    assert!(value["sealedAtUs"].as_u64().unwrap() >= last);
    trace.record(Stage::BuildOk);
    assert_eq!(memory.attempted.load(SeqCst), 8);
}

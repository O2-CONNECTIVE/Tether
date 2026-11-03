use super::MacOutput;
use crate::mac_status::state::{FeedFault, FeedState};
use serde_json::{json, Value};

fn frame(changes: &[(&str, Value)]) -> Vec<u8> {
    let mut value = json!({
        "version": 1, "source_continuous_ms": 100000, "seq": 1,
        "phase": "connecting", "reason": "none", "grant_remaining_ms": 2000,
        "starting_workers": 0, "running_workers": 0, "stopping_workers": 0,
        "cleanup": "pending", "terminal_cleanup_unknown": false, "terminal": false
    });
    for (key, field) in changes {
        value[*key] = field.clone();
    }
    serde_json::to_vec(&value).unwrap()
}
fn output(state: &FeedState, now: f64) -> Value {
    serde_json::to_value(MacOutput::synthetic(state.snapshot(), now)).unwrap()
}
#[test]
fn output_uses_validated_bytes_and_bounds_lifetime_by_deadline() {
    let mut state = FeedState::default();
    state.ingest(&frame(&[]), 100.0);
    let value = output(&state, 100.5);
    assert_eq!(value["state"], "synthetic-observation");
    assert_eq!(value["view"]["heading"], "接続を確認中");
    assert_eq!(value["validForMs"], 1500);
    assert_eq!(value["cleanupUnknown"], false);
    assert_eq!(value["terminalHistory"], false);
}
#[test]
fn stale_and_expiry_cannot_gain_a_new_budget() {
    let mut state = FeedState::default();
    state.ingest(&frame(&[("grant_remaining_ms", json!(10000))]), 100.0);
    assert_eq!(output(&state, 102.5)["validForMs"], 500);
    for now in [103.0, 103.001, f64::NAN, f64::INFINITY] {
        assert_eq!(output(&state, now)["validForMs"], 0);
    }
    let mut expired = FeedState::default();
    expired.ingest(&frame(&[]), 100.0);
    assert_eq!(output(&expired, 102.0)["view"]["heading"], "承認時間の終了・停止は未確認");
    assert_eq!(output(&expired, 102.0)["validForMs"], 0);
}
#[test]
fn null_or_later_deadline_does_not_extend_output_budget() {
    for remaining in [Value::Null, json!(10000)] {
        let mut state = FeedState::default();
        state.ingest(&frame(&[]), 100.0);
        state.ingest(
            &frame(&[
                ("seq", json!(2)),
                ("source_continuous_ms", json!(101000)),
                ("grant_remaining_ms", remaining),
            ]),
            101.0,
        );
        assert_eq!(output(&state, 101.0)["validForMs"], 1000);
    }
}
#[test]
fn duplicate_keys_and_invalid_utf8_are_not_normalized_to_valid_output() {
    let bytes = frame(&[]);
    let mut duplicate = b"{\"version\":1,".to_vec();
    duplicate.extend(&bytes[1..]);
    let mut utf8 = bytes.clone();
    utf8[1] = 255;
    for bytes in [duplicate, utf8] {
        let mut state = FeedState::default();
        state.ingest(&bytes, 100.0);
        assert_eq!(state.snapshot().fault, Some(FeedFault::Malformed));
        assert_eq!(output(&state, 100.0)["validForMs"], 0);
        assert_ne!(output(&state, 100.0)["view"]["icon"], "checking");
    }
}
#[test]
fn replay_poison_remains_and_a_presentation_read_does_not_reset_sequence() {
    let mut state = FeedState::default();
    state.ingest(&frame(&[]), 100.0);
    let _ = output(&state, 100.0);
    state.ingest(&frame(&[]), 100.1);
    assert_eq!(state.snapshot().fault, Some(FeedFault::Sequence));
    assert_eq!(output(&state, 100.1)["validForMs"], 0);
}
#[test]
fn eof_alone_is_not_terminal_history_or_cleanup_confirmation() {
    let mut state = FeedState::default();
    state.ingest(&frame(&[]), 100.0);
    state.fail(FeedFault::Eof);
    let value = output(&state, 100.0);
    assert_eq!(value["terminalHistory"], false);
    assert_eq!(value["view"]["icon"], "warning");
    assert_eq!(value["validForMs"], 0);
}
#[test]
fn terminal_eof_history_is_retained_separately_from_current_lifetime() {
    let mut state = FeedState::default();
    state.ingest(
        &frame(&[
            ("phase", json!("closed")),
            ("terminal", json!(true)),
            ("cleanup", json!("confirmed")),
            ("grant_remaining_ms", json!(0)),
        ]),
        100.0,
    );
    state.fail(FeedFault::Eof);
    let value = output(&state, 1000.0);
    assert_eq!(value["terminalHistory"], true);
    assert_eq!(value["validForMs"], 0);
    assert_eq!(value["view"]["heading"], "状態配信終了・最終報告の履歴");
}
#[test]
fn cleanup_unknown_survives_eof_and_later_faults() {
    let mut state = FeedState::default();
    state.ingest(
        &frame(&[
            ("phase", json!("failed")),
            ("terminal", json!(true)),
            ("reason", json!("terminal_cleanup_unknown")),
            ("cleanup", json!("unknown")),
            ("terminal_cleanup_unknown", json!(true)),
        ]),
        100.0,
    );
    for fault in [FeedFault::Eof, FeedFault::Malformed, FeedFault::Read] {
        state.fail(fault);
        let value = output(&state, 1000.0);
        assert_eq!(value["cleanupUnknown"], true);
        assert_eq!(value["view"]["heading"], "回収を確認できません");
        assert_eq!(value["validForMs"], 0);
    }
}

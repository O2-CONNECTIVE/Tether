use super::{present, Icon};
use crate::mac_status::state::{FeedFault, FeedState};
use serde_json::{json, Value};

// Test inputs still enter the real byte decoder and FeedState. Never construct
// a Snapshot directly or cast a synthetic presentation input into Mac state.
fn input(changes: &[(&str, Value)]) -> Vec<u8> {
    let mut frame = json!({
        "version": 1, "source_continuous_ms": 100000, "seq": 1,
        "phase": "connected", "reason": "none", "grant_remaining_ms": 2000,
        "starting_workers": 0, "running_workers": 0, "stopping_workers": 0,
        "cleanup": "pending", "terminal_cleanup_unknown": false, "terminal": false
    });
    for (key, value) in changes {
        frame[*key] = value.clone();
    }
    serde_json::to_vec(&frame).unwrap()
}

fn ingest(state: &mut FeedState, changes: &[(&str, Value)], now: f64) {
    state.ingest(&input(changes), now);
    assert!(state.snapshot().fault.is_none(), "invalid test input");
}

#[test]
fn missing_and_unfresh_observations_never_show_progress() {
    let mut state = FeedState::default();
    assert_eq!(
        present(state.snapshot(), 100.0).heading,
        "状態情報を待っています"
    );
    ingest(&mut state, &[("phase", json!("connecting"))], 100.0);
    for now in [99.0, 103.001, f64::NAN, f64::INFINITY] {
        let output = present(state.snapshot(), now);
        assert_eq!(output.icon, Icon::Warning);
        assert_eq!(output.heading, "状態の更新を確認できません");
    }
}

#[test]
fn freshness_boundary_and_expired_deadline_are_distinct() {
    let mut state = FeedState::default();
    ingest(&mut state, &[("grant_remaining_ms", json!(10000))], 100.0);
    assert_eq!(present(state.snapshot(), 103.0).heading, "接続中の報告");
    assert_eq!(present(state.snapshot(), 103.001).icon, Icon::Warning);
    let mut expired = FeedState::default();
    ingest(&mut expired, &[("phase", json!("connecting"))], 100.0);
    let output = present(expired.snapshot(), 102.0);
    assert_eq!(output.heading, "承認時間の終了・停止は未確認");
    assert_eq!(output.icon, Icon::Warning);
}

#[test]
fn setup_phase_output_is_not_synthetic_or_authority() {
    for (phase, heading, icon) in [
        ("checking_config", "起動設定を確認中", Icon::Checking),
        ("loading_key", "接続用の鍵を確認中", Icon::Checking),
        ("preparing", "この Mac の実行環境を準備中", Icon::Checking),
        (
            "awaiting_approval",
            "起動したターミナルで承認してください",
            Icon::Neutral,
        ),
        ("connecting", "接続を確認中", Icon::Checking),
        ("connected", "接続中の報告", Icon::Neutral),
        ("stopping", "停止・回収中", Icon::Neutral),
    ] {
        let mut state = FeedState::default();
        ingest(&mut state, &[("phase", json!(phase))], 100.0);
        let output = present(state.snapshot(), 100.0);
        assert_eq!(output.heading, heading);
        assert_eq!(output.icon, icon);
        assert!(!output.heading.contains("見本"));
    }
}

#[test]
fn eof_without_terminal_and_read_errors_do_not_continue_progress() {
    for fault in [
        FeedFault::Eof,
        FeedFault::Read,
        FeedFault::Malformed,
        FeedFault::Stopped,
    ] {
        let mut state = FeedState::default();
        ingest(&mut state, &[("phase", json!("connecting"))], 100.0);
        state.fail(fault);
        assert_eq!(present(state.snapshot(), 100.0).icon, Icon::Warning);
        assert_eq!(
            present(state.snapshot(), 100.0).heading,
            "状態の更新を確認できません"
        );
    }
}

#[test]
fn terminal_eof_retains_history_but_not_current_cleanup_proof() {
    for (cleanup, detail) in [
        ("confirmed", "最終報告：回収確認済み。現在の接続や OS 全体の回収を独立に確認したものではありません。"),
        ("pending", "最終報告：回収は未確認です。確認が続いているとは限りません。"),
        ("not_started", "最終報告：実行は開始されていません。現在の接続を示す表示ではありません。"),
    ] {
        let mut state = FeedState::default();
        ingest(&mut state, &[
            ("phase", json!("closed")), ("terminal", json!(true)),
            ("cleanup", json!(cleanup)), ("grant_remaining_ms", json!(0)),
        ], 100.0);
        assert_eq!(present(state.snapshot(), 100.0).heading, "最終報告");
        state.fail(FeedFault::Eof);
        let output = present(state.snapshot(), 1000.0);
        assert_eq!(output.heading, "状態配信終了・最終報告の履歴");
        assert_eq!(output.detail, detail);
        assert_eq!(output.icon, Icon::Neutral);
        state.fail(FeedFault::Read);
        assert_eq!(present(state.snapshot(), 1000.0).icon, Icon::Warning);
    }
}

#[test]
fn unknown_overrides_eof_staleness_and_all_later_faults() {
    let mut state = FeedState::default();
    ingest(
        &mut state,
        &[
            ("phase", json!("failed")),
            ("terminal", json!(true)),
            ("reason", json!("terminal_cleanup_unknown")),
            ("terminal_cleanup_unknown", json!(true)),
            ("cleanup", json!("unknown")),
        ],
        100.0,
    );
    for fault in [FeedFault::Eof, FeedFault::Read, FeedFault::Malformed] {
        state.fail(fault);
        let output = present(state.snapshot(), 1000.0);
        assert_eq!(output.heading, "回収を確認できません");
        assert_eq!(output.icon, Icon::Warning);
    }
    let mut nonterminal = FeedState::default();
    ingest(&mut nonterminal, &[("cleanup", json!("unknown"))], 100.0);
    assert!(!nonterminal.snapshot().sticky_unknown);
    assert_eq!(
        present(nonterminal.snapshot(), 100.0).heading,
        "回収を確認できません"
    );
}

#[test]
fn null_remaining_keeps_prior_deadline_and_newer_remaining_cannot_extend_it() {
    for remaining in [Value::Null, json!(10000)] {
        let mut state = FeedState::default();
        ingest(&mut state, &[], 100.0);
        ingest(
            &mut state,
            &[
                ("seq", json!(2)),
                ("source_continuous_ms", json!(101000)),
                ("grant_remaining_ms", remaining),
            ],
            101.0,
        );
        assert_eq!(
            present(state.snapshot(), 102.0).heading,
            "承認時間の終了・停止は未確認"
        );
    }
}

#[test]
fn setup_failure_eof_is_history_and_not_an_approval_action() {
    for (reason, heading) in [
        ("config_error", "設定を確認できませんでした"),
        ("key_error", "接続用の鍵を確認できませんでした"),
        ("preparation_error", "この Mac の準備ができませんでした"),
        ("connection_error", "接続を準備できませんでした"),
        ("approval_not_completed", "承認は完了しませんでした"),
    ] {
        let mut state = FeedState::default();
        ingest(
            &mut state,
            &[
                ("phase", json!("failed")),
                ("terminal", json!(true)),
                ("reason", json!(reason)),
                ("cleanup", json!("not_started")),
                ("grant_remaining_ms", Value::Null),
            ],
            100.0,
        );
        state.fail(FeedFault::Eof);
        let output = present(state.snapshot(), 1000.0);
        assert_eq!(output.heading, heading);
        assert!(output.detail.starts_with("最終報告："));
        assert_eq!(output.icon, Icon::Neutral);
    }
}

#[test]
fn terminal_replay_is_not_revived_and_mapping_does_not_mutate_state() {
    let mut state = FeedState::default();
    ingest(
        &mut state,
        &[
            ("phase", json!("closed")),
            ("terminal", json!(true)),
            ("cleanup", json!("confirmed")),
            ("grant_remaining_ms", json!(0)),
        ],
        100.0,
    );
    let before = format!("{:?}", state.snapshot());
    let _ = present(state.snapshot(), 100.0);
    assert_eq!(format!("{:?}", state.snapshot()), before);
    state.ingest(&input(&[("seq", json!(2))]), 101.0);
    assert_eq!(state.snapshot().fault, Some(FeedFault::Sequence));
    assert_eq!(present(state.snapshot(), 101.0).icon, Icon::Warning);
}

#[test]
fn nonterminal_confirmed_cleanup_never_becomes_terminal_success() {
    let mut state = FeedState::default();
    ingest(&mut state, &[("cleanup", json!("confirmed"))], 100.0);
    assert_eq!(present(state.snapshot(), 100.0).heading, "接続中の報告");
    state.fail(FeedFault::Eof);
    assert_eq!(
        present(state.snapshot(), 100.0).heading,
        "状態の更新を確認できません"
    );
}

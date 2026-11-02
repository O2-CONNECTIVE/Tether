use super::state::Snapshot;

// Internal presentation values, not an IPC schema or a synthetic input frame.
// These three fields correspond to StatusContent's output-only props.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Icon {
    Checking,
    Neutral,
    Warning,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct MacPresentation {
    pub(crate) heading: &'static str,
    pub(crate) detail: &'static str,
    pub(crate) icon: Icon,
}

fn view(heading: &'static str, detail: &'static str, icon: Icon) -> MacPresentation {
    MacPresentation {
        heading,
        detail,
        icon,
    }
}

// Only callers holding the validated Mac state can enter this mapper. No JSON,
// JS clock, IO, authority, synthetic sample conversion or state mutation here.
pub(crate) fn present(snapshot: &Snapshot, now: f64) -> MacPresentation {
    let frame = snapshot.frame.as_ref();
    if snapshot.sticky_unknown || frame.is_some_and(|frame| frame.cleanup == "unknown") {
        return view(
            "回収を確認できません",
            "操作が終わっていても、安全に回収されたとは限りません。",
            Icon::Warning,
        );
    }
    let Some(frame) = frame else {
        return view(
            "状態情報を待っています",
            "接続や停止を確認できません。表示から状態を推定しないでください。",
            Icon::Neutral,
        );
    };
    let fresh = snapshot.fresh(now);
    let terminal_history = frame.terminal && snapshot.source_closed();
    if !fresh && !terminal_history {
        return view(
            "状態の更新を確認できません",
            "古い報告を現在の状態として表示しません。接続や停止は未確認です。",
            Icon::Warning,
        );
    }
    if frame.terminal {
        return terminal_view(&frame.reason, &frame.cleanup, terminal_history);
    }
    // A stopped countdown is not proof of stopping or cleanup. Do not keep
    // showing setup progress from a frame whose retained deadline has elapsed.
    if snapshot.remaining(now) == Some(0) {
        return view(
            "承認時間の終了・停止は未確認",
            "残り時間が 0 秒でも停止や回収の証明にはなりません。",
            Icon::Warning,
        );
    }
    if frame.stopping > 0 || frame.phase == "stopping" {
        return view(
            "停止・回収中",
            "報告上は回収待ちです。停止や回収の完了はまだ確認できません。",
            Icon::Neutral,
        );
    }
    match frame.phase.as_str() {
        "checking_config" => view(
            "起動設定を確認中",
            "この Mac の保護された設定を確認しています。",
            Icon::Checking,
        ),
        "loading_key" => view(
            "接続用の鍵を確認中",
            "登録済みの鍵を確認しています。新しい鍵は作りません。",
            Icon::Checking,
        ),
        "preparing" => view(
            "この Mac の実行環境を準備中",
            "まだ操作は許可されていません。準備が終わると本人の承認を待ちます。",
            Icon::Checking,
        ),
        "awaiting_approval" => view(
            "起動したターミナルで承認してください",
            "ターミナルの説明を確認してください。この画面では承認できません。",
            Icon::Neutral,
        ),
        "connecting" => view(
            "接続を確認中",
            "相手の認証と接続の成立を確認しています。まだ接続完了ではありません。",
            Icon::Checking,
        ),
        "connected" => view(
            "接続中の報告",
            "受信した状態の観測表示です。新しい操作を許可するものではありません。",
            Icon::Neutral,
        ),
        _ => view(
            "状態は未確認です",
            "接続や停止を確認できません。",
            Icon::Warning,
        ),
    }
}

fn terminal_view(reason: &str, cleanup: &str, source_closed: bool) -> MacPresentation {
    let heading = match reason {
        "config_error" => "設定を確認できませんでした",
        "key_error" => "接続用の鍵を確認できませんでした",
        "preparation_error" => "この Mac の準備ができませんでした",
        "connection_error" => "接続を準備できませんでした",
        "approval_not_completed" => "承認は完了しませんでした",
        _ if source_closed => "状態配信終了・最終報告の履歴",
        _ => "最終報告",
    };
    let detail = match cleanup {
        "confirmed" => "最終報告：回収確認済み。現在の接続や OS 全体の回収を独立に確認したものではありません。",
        "not_started" => "最終報告：実行は開始されていません。現在の接続を示す表示ではありません。",
        _ => "最終報告：回収は未確認です。確認が続いているとは限りません。",
    };
    view(heading, detail, Icon::Neutral)
}

#[cfg(test)]
#[path = "presentation_tests.rs"]
mod tests;

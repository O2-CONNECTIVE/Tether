use super::*;
use serde_json::{json, Value};

fn ipc(
    output: StatusPresentation,
    label: &str,
    origin: &str,
    request: Value,
) -> Result<Value, Value> {
    let app =
        commands_with_presentation(tauri::test::mock_builder(), ShellRuntime::default(), output)
            .build(tauri::generate_context!())
            .unwrap();
    let window = tauri::WebviewWindowBuilder::new(&app, label, Default::default())
        .build()
        .unwrap();
    tauri::test::get_ipc_response(
        &window,
        tauri::webview::InvokeRequest {
            cmd: "get_status_presentation".into(),
            callback: tauri::ipc::CallbackFn(0),
            error: tauri::ipc::CallbackFn(1),
            url: origin.parse().unwrap(),
            body: tauri::ipc::InvokeBody::Json(json!({"request": request})),
            headers: Default::default(),
            invoke_key: tauri::test::INVOKE_KEY.into(),
        },
    )
    .map(|response| response.deserialize::<Value>().unwrap())
}

#[test]
fn default_command_route_is_compiled_target_and_never_fixture_observation() {
    let output = ipc(
        StatusPresentation::default(),
        "main",
        "http://tauri.localhost",
        json!({"protocolVersion": 1}),
    )
    .unwrap();
    assert_eq!(output["protocolVersion"], 1);
    if cfg!(target_os = "windows") {
        assert_eq!(
            output,
            json!({"protocolVersion": 1, "source": "windows-shell"})
        );
    } else if cfg!(target_os = "macos") {
        assert_eq!(
            output,
            json!({"protocolVersion": 1, "source": "mac-status", "state": "source-unavailable"})
        );
    } else {
        assert_eq!(
            output,
            json!({"protocolVersion": 1, "source": "unsupported"})
        );
    }
}

#[test]
fn request_cannot_select_platform_or_inject_bytes_clock_and_source() {
    for request in [
        json!({"protocolVersion": 2}),
        json!({"protocolVersion": "1"}),
        json!({}),
        json!({"protocolVersion": 1, "platform": "macos"}),
        json!({"protocolVersion": 1, "source": "synthetic-observation"}),
        json!({"protocolVersion": 1, "bytes": "{}"}),
        json!({"protocolVersion": 1, "now": 100.0}),
    ] {
        assert!(ipc(
            StatusPresentation::default(),
            "main",
            "http://tauri.localhost",
            request,
        )
        .is_err());
    }
}

#[test]
fn new_read_command_rejects_unauthorized_window_and_remote_origin() {
    for (label, origin) in [
        ("other", "http://tauri.localhost"),
        ("main", "https://example.invalid"),
    ] {
        assert!(ipc(
            StatusPresentation::default(),
            label,
            origin,
            json!({"protocolVersion": 1}),
        )
        .is_err());
    }
}

#[test]
fn validated_finite_fixture_output_crosses_the_actual_mock_tauri_command() {
    let mut state = crate::mac_status::state::FeedState::default();
    state.ingest(
        br#"{"version":1,"source_continuous_ms":100000,"seq":1,"phase":"connecting","reason":"none","grant_remaining_ms":2000,"starting_workers":0,"running_workers":0,"stopping_workers":0,"cleanup":"pending","terminal_cleanup_unknown":false,"terminal":false}"#,
        100.0,
    );
    assert_eq!(state.snapshot().fault, None);
    let output = ipc(
        StatusPresentation::synthetic(state.snapshot(), 100.5),
        "main",
        "http://tauri.localhost",
        json!({"protocolVersion": 1}),
    )
    .unwrap();
    assert_eq!(output["source"], "mac-status");
    assert_eq!(output["state"], "synthetic-observation");
    assert_eq!(output["view"]["heading"], "接続を確認中");
    assert_eq!(output["validForMs"], 1500);
    assert_eq!(output["cleanupUnknown"], false);
    assert_eq!(output["terminalHistory"], false);
}

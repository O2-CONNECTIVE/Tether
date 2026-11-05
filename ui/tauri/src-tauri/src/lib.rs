#[allow(
    dead_code,
    reason = "Mac input prerequisites are not attached to production startup or IPC"
)]
mod mac_status;

#[allow(
    dead_code,
    reason = "resident IO ownership has no trusted launch factory or production IPC binding"
)]
mod resident_port;

#[cfg(target_os = "macos")]
#[allow(
    dead_code,
    reason = "outer resident owner awaits trusted launch and GUI binding"
)]
mod standby_lifecycle;

#[cfg(any(test, feature = "startup-diagnostics"))]
#[allow(dead_code, reason = "startup diagnostics are an opt-in test variant")]
mod startup_diagnostics;

#[cfg(windows)]
#[allow(
    dead_code,
    reason = "authenticated Windows client bootstrap is not wired"
)]
mod windows_client;

use serde::{Deserialize, Serialize};
mod shell_runtime;
mod status_presentation;
use shell_runtime::ShellRuntime;
use status_presentation::StatusPresentation;

#[allow(
    dead_code,
    reason = "control codec is not wired while runtime activation is closed"
)]
mod control_codec;

#[allow(
    dead_code,
    reason = "internal process boundary is not wired while the product gate is closed"
)]
mod owned_process;

#[allow(
    dead_code,
    reason = "coordinator is not wired while runtime activation is closed"
)]
mod control_coordinator;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct StatusRequest {
    protocol_version: u8,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ShellStatus {
    protocol_version: u8,
    integration: &'static str,
    can_start: bool,
    can_stop: bool,
    termination: &'static str,
}

impl ShellStatus {
    fn unwired() -> Self {
        Self {
            protocol_version: 1,
            integration: "not_connected",
            can_start: false,
            can_stop: false,
            termination: "unconfirmed",
        }
    }
}

#[tauri::command]
fn get_shell_status(
    request: StatusRequest,
    runtime: tauri::State<'_, ShellRuntime>,
) -> Result<ShellStatus, &'static str> {
    if request.protocol_version != 1 {
        return Err("UNSUPPORTED_SHELL_VERSION");
    }
    runtime.status()
}

#[tauri::command]
fn get_status_presentation(
    request: StatusRequest,
    output: tauri::State<'_, StatusPresentation>,
) -> Result<StatusPresentation, &'static str> {
    if request.protocol_version != 1 {
        return Err("UNSUPPORTED_PRESENTATION_VERSION");
    }
    Ok(output.inner().clone())
}

fn commands<R: tauri::Runtime>(builder: tauri::Builder<R>) -> tauri::Builder<R> {
    commands_with_runtime(builder, ShellRuntime::default())
}

fn commands_with_runtime<R: tauri::Runtime>(
    builder: tauri::Builder<R>,
    runtime: ShellRuntime,
) -> tauri::Builder<R> {
    commands_with_presentation(builder, runtime, StatusPresentation::default())
}

fn commands_with_presentation<R: tauri::Runtime>(
    builder: tauri::Builder<R>,
    runtime: ShellRuntime,
    output: StatusPresentation,
) -> tauri::Builder<R> {
    builder
        .manage(runtime)
        .manage(output)
        .invoke_handler(tauri::generate_handler![
            get_shell_status,
            get_status_presentation
        ])
}

pub fn run() {
    #[cfg(any(test, feature = "startup-diagnostics"))]
    let startup = startup_diagnostics::Trace::start();
    commands(tauri::Builder::default())
        .setup(move |app| {
            #[cfg(any(test, feature = "startup-diagnostics"))]
            startup.record(startup_diagnostics::Stage::SetupEnter);
            let builder = tauri::WebviewWindowBuilder::new(
                app,
                "main",
                tauri::WebviewUrl::App("index.html".into()),
            )
            .title("Rescue Host（未配線 shell）")
            .inner_size(900.0, 780.0)
            .min_inner_size(360.0, 480.0)
            .on_navigation(|url| {
                if !url.username().is_empty() || url.password().is_some() || url.port().is_some() {
                    return false;
                }
                #[cfg(windows)]
                let bundled = url.scheme() == "http" && url.host_str() == Some("tauri.localhost");
                #[cfg(not(windows))]
                let bundled = url.scheme() == "tauri" && url.host_str() == Some("localhost");
                bundled
            });
            #[cfg(any(test, feature = "startup-diagnostics"))]
            let builder = {
                let page_trace = startup.clone();
                builder.on_page_load(move |_webview, payload| {
                    if matches!(payload.event(), tauri::webview::PageLoadEvent::Finished) {
                        page_trace.record(startup_diagnostics::Stage::PageFinished);
                    }
                })
            };
            #[cfg(any(test, feature = "startup-diagnostics"))]
            startup.record(startup_diagnostics::Stage::BuildStart);
            let result = builder.build();
            #[cfg(any(test, feature = "startup-diagnostics"))]
            startup.record(if result.is_ok() {
                startup_diagnostics::Stage::BuildOk
            } else {
                startup_diagnostics::Stage::BuildError
            });
            result?;
            #[cfg(any(test, feature = "startup-diagnostics"))]
            startup.record(startup_diagnostics::Stage::SetupReturn);
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("failed to run Rescue Host shell");
}

#[cfg(test)]
const LOCAL_TEST_ORIGIN: &str = if cfg!(target_os = "windows") {
    "http://tauri.localhost"
} else {
    "tauri://localhost"
};

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn ipc(
        label: &str,
        origin: &str,
        command: &str,
        body: serde_json::Value,
    ) -> Result<serde_json::Value, serde_json::Value> {
        ipc_with_runtime(ShellRuntime::default(), label, origin, command, body)
    }

    fn ipc_with_runtime(
        runtime: ShellRuntime,
        label: &str,
        origin: &str,
        command: &str,
        body: serde_json::Value,
    ) -> Result<serde_json::Value, serde_json::Value> {
        let app = commands_with_runtime(tauri::test::mock_builder(), runtime)
            // Keep generated capabilities; omit runtime-only plist embedding.
            .build(tauri::generate_context!(test = true))
            .unwrap();
        let window = tauri::WebviewWindowBuilder::new(&app, label, Default::default())
            .build()
            .unwrap();
        tauri::test::get_ipc_response(
            &window,
            tauri::webview::InvokeRequest {
                cmd: command.into(),
                callback: tauri::ipc::CallbackFn(0),
                error: tauri::ipc::CallbackFn(1),
                url: origin.parse().unwrap(),
                body: tauri::ipc::InvokeBody::Json(body),
                headers: Default::default(),
                invoke_key: tauri::test::INVOKE_KEY.into(),
            },
        )
        .map(|response| response.deserialize::<serde_json::Value>().unwrap())
    }

    fn expected_control_context() -> control_codec::model::Bootstrap {
        serde_json::from_value(json!({
            "protocol": "rescue-host-control/1", "generation": "1",
            "identity": {"launchId": "launch-001", "epoch": "1", "policyDigest": "a".repeat(64)},
            "binding": {"artifactSha256": "b".repeat(64), "channelNonce": "c".repeat(64)}
        }))
        .unwrap()
    }

    #[test]
    fn active_peer_claim_and_reader_eof_do_not_enable_gui_operations() {
        use control_coordinator::connection::{ReaderConnection, ReaderProgress};
        use control_coordinator::reader::{ReaderEnd, ReaderTask};
        use control_coordinator::Conversation;
        use std::io::Cursor;
        use std::thread;
        use std::time::{Duration, Instant};
        let expected = expected_control_context();
        let mut ack = serde_json::to_value(&expected).unwrap();
        ack["kind"] = json!("hello_ack");
        let active = json!({
            "protocol": "rescue-host-control/1", "kind": "state_changed", "generation": "1",
            "identity": ack["identity"], "sequence": "1",
            "payload": {"goState": "active", "pending": "0", "active": "1", "owned": "1", "goCloseConfirmed": false}
        });
        // Finite memory claims are not an authenticated service or Go activation.
        let mut bytes = serde_json::to_vec(&ack).unwrap();
        bytes.push(b'\n');
        bytes.extend(serde_json::to_vec(&active).unwrap());
        bytes.push(b'\n');
        let reader = ReaderTask::start(Cursor::new(bytes)).unwrap();
        let mut connection = ReaderConnection::new(Conversation::new(expected), reader);
        connection.hello().unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            assert!(Instant::now() < deadline, "finite peer claim deadline");
            match connection.poll() {
                ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
                ReaderProgress::Frame => {}
                ReaderProgress::Joined { end, fault } => {
                    assert_eq!(end, ReaderEnd::Eof);
                    assert_eq!(fault, None);
                    break;
                }
                event => panic!("unexpected claim event: {event:?}"),
            }
        }
        assert_eq!(
            connection.target().peer_snapshot_claim().unwrap().go_state,
            control_codec::scalar::GoState::Active
        );
        let response = ipc_with_runtime(
            ShellRuntime::monitoring(connection),
            "main",
            LOCAL_TEST_ORIGIN,
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}}),
        )
        .unwrap();
        assert_eq!(
            response,
            json!({
                "protocolVersion": 1, "integration": "not_connected",
                "canStart": false, "canStop": false, "termination": "unconfirmed"
            })
        );
    }

    #[test]
    fn public_ipc_preserves_a_joined_reader_fault_instead_of_hiding_it() {
        use control_coordinator::connection::{ReaderConnection, ReaderProgress};
        use control_coordinator::reader::ReaderTask;
        use control_coordinator::Conversation;
        use std::io::Cursor;
        use std::thread;
        use std::time::{Duration, Instant};
        let expected = expected_control_context();
        let reader = ReaderTask::start(Cursor::new(b"not-json\n")).unwrap();
        let mut connection = ReaderConnection::new(Conversation::new(expected), reader);
        connection.hello().unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            assert!(Instant::now() < deadline, "finite IPC worker deadline");
            match connection.poll() {
                ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
                ReaderProgress::Fault(control_codec::scalar::Code::InvalidMessage) => {}
                ReaderProgress::Joined { fault, .. } => {
                    assert_eq!(fault, Some(control_codec::scalar::Code::InvalidMessage));
                    break;
                }
                event => panic!("unexpected reader event: {event:?}"),
            }
        }
        let response = ipc_with_runtime(
            ShellRuntime::monitoring(connection),
            "main",
            LOCAL_TEST_ORIGIN,
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}}),
        );
        assert_eq!(response, Err(json!("INVALID_MESSAGE")));
    }

    #[test]
    fn public_ipc_reports_unwired_and_cannot_start_or_confirm_termination() {
        let response = ipc(
            "main",
            LOCAL_TEST_ORIGIN,
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}}),
        )
        .unwrap();
        assert_eq!(
            response,
            json!({
                "protocolVersion": 1, "integration": "not_connected",
                "canStart": false, "canStop": false, "termination": "unconfirmed"
            })
        );
    }

    #[test]
    fn rejects_unsupported_version() {
        let error = ipc(
            "main",
            LOCAL_TEST_ORIGIN,
            "get_shell_status",
            json!({"request": {"protocolVersion": 2}}),
        )
        .unwrap_err();
        assert_eq!(error, json!("UNSUPPORTED_SHELL_VERSION"));
    }

    #[test]
    fn rejects_unknown_request_field_and_wrong_type() {
        for request in [
            json!({"protocolVersion": 1, "executable": "not-allowed"}),
            json!({"protocolVersion": "1"}),
        ] {
            assert!(ipc(
                "main",
                LOCAL_TEST_ORIGIN,
                "get_shell_status",
                json!({"request": request})
            )
            .is_err());
        }
    }

    #[test]
    fn denies_window_without_capability() {
        assert!(ipc(
            "other",
            LOCAL_TEST_ORIGIN,
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}})
        )
        .is_err());
    }

    #[test]
    fn denies_remote_origin() {
        assert!(ipc(
            "main",
            "https://example.invalid",
            "get_shell_status",
            json!({"request": {"protocolVersion": 1}})
        )
        .is_err());
    }

    #[test]
    fn has_no_start_session_command() {
        assert!(ipc("main", LOCAL_TEST_ORIGIN, "start_session", json!({})).is_err());
    }

    #[test]
    fn shared_shell_v1_fixture_matches_public_mock_ipc() {
        let fixture_text = include_str!("../../contracts/shell-status-v1.json");
        let fixture: serde_json::Value = serde_json::from_str(fixture_text).unwrap();
        assert_eq!(fixture["scope"], "unwired-shell-status-v1");
        let command = fixture["command"].as_str().unwrap();
        let response = ipc(
            "main",
            LOCAL_TEST_ORIGIN,
            command,
            json!({"request": fixture["request"]}),
        )
        .unwrap();
        assert_eq!(response, fixture["response"]);
        let cases = fixture["rejectedRequests"].as_array().unwrap();
        assert_eq!(cases.len(), 8);
        let mut ids = std::collections::HashSet::new();
        for case in cases {
            let id = case["id"].as_str().unwrap();
            assert!(!id.is_empty());
            assert!(ids.insert(id));
            assert!(case.get("payload").is_some());
            assert!(
                ipc(
                    "main",
                    LOCAL_TEST_ORIGIN,
                    command,
                    json!({"request": case["payload"]}),
                )
                .is_err(),
                "{id}"
            );
        }
    }
}

#[cfg(test)]
mod status_presentation_tests;

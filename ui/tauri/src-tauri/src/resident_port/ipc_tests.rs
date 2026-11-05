use super::*;
use serde_json::{json, Value};
use tauri::Manager;

fn app(port: ResidentPort) -> tauri::App<tauri::test::MockRuntime> {
    let app = crate::commands_with_ports(
        tauri::test::mock_builder(),
        crate::ShellRuntime::default(),
        crate::StatusPresentation::default(),
        port,
    )
    .build(tauri::generate_context!(
        "tests/resident-context/tauri.conf.json",
        test = true
    ))
    .unwrap();
    let selected = serde_json::to_value(&app.config().app.security.capabilities).unwrap();
    assert_eq!(selected[0]["identifier"], "resident-source-tests");
    app
}
fn read(
    app: &tauri::App<tauri::test::MockRuntime>,
    label: &str,
    origin: &str,
    command: &str,
    request: Value,
) -> Result<Value, Value> {
    let window = app.get_webview_window(label).unwrap_or_else(|| {
        tauri::WebviewWindowBuilder::new(app, label, Default::default())
            .build()
            .unwrap()
    });
    tauri::test::get_ipc_response(
        &window,
        tauri::webview::InvokeRequest {
            cmd: command.into(),
            callback: tauri::ipc::CallbackFn(0),
            error: tauri::ipc::CallbackFn(1),
            url: origin.parse().unwrap(),
            body: tauri::ipc::InvokeBody::Json(json!({"request":request})),
            headers: Default::default(),
            invoke_key: tauri::test::INVOKE_KEY.into(),
        },
    )
    .map(|r| r.deserialize().unwrap())
}
fn intent() -> Value {
    json!({"protocolVersion":1,"requestId":1,"expectedRevision":0,"intent":"on"})
}

#[test]
fn explicit_ipc_unbound_status_and_receipt_never_claim_a_connected_source() {
    let app = app(ResidentPort::default());
    let status = read(
        &app,
        "main",
        crate::LOCAL_TEST_ORIGIN,
        "get_resident_status",
        json!({"protocolVersion":1}),
    )
    .unwrap();
    assert_eq!(
        status,
        serde_json::to_value(model::Status::unbound()).unwrap()
    );
    assert_eq!(
        read(
            &app,
            "main",
            crate::LOCAL_TEST_ORIGIN,
            "request_resident_intent",
            intent()
        )
        .unwrap(),
        json!({"protocolVersion":1,"requestId":1,"intent":"on","disposition":"unavailable"})
    );
}

#[test]
fn explicit_ipc_rejects_invalid_schema_and_has_no_binding_or_policy_command() {
    let app = app(ResidentPort::default());
    let call = |command, request| read(&app, "main", crate::LOCAL_TEST_ORIGIN, command, request);
    for request in [
        json!({}),
        json!({"protocolVersion":2}),
        json!({"protocolVersion":"1"}),
        json!({"protocolVersion":1,"path":"/fake"}),
        json!({"protocolVersion":1,"source":"authenticated"}),
    ] {
        assert!(call("get_resident_status", request).is_err());
    }
    for (field, value) in [
        ("path", json!("/fake")),
        ("command", json!("shell")),
        ("credentials", json!("never")),
        ("requestId", json!(0)),
        ("requestId", json!("1")),
        ("requestId", json!(-1)),
        ("requestId", json!(9_007_199_254_740_992_u64)),
        ("expectedRevision", json!(9_007_199_254_740_992_u64)),
        ("intent", json!("restart")),
        ("protocolVersion", json!(2)),
    ] {
        let mut request = intent();
        request[field] = value;
        assert!(call("request_resident_intent", request).is_err(), "{field}");
    }
    for command in [
        "bind_resident_source",
        "launch_resident",
        "set_resident_policy",
    ] {
        assert!(call(command, json!({"path":"/fake"})).is_err());
    }
}
#[test]
fn explicit_capability_rejects_other_windows_remote_and_opposite_platform_origin() {
    let app = app(ResidentPort::default());
    let opposite = if cfg!(target_os = "windows") {
        "tauri://localhost"
    } else {
        "http://tauri.localhost"
    };
    for (label, origin) in [
        ("other", crate::LOCAL_TEST_ORIGIN),
        ("main", "https://example.invalid"),
        ("main", opposite),
    ] {
        assert!(read(
            &app,
            label,
            origin,
            "get_resident_status",
            json!({"protocolVersion":1})
        )
        .is_err());
        assert!(read(&app, label, origin, "request_resident_intent", intent()).is_err());
    }
}

#[cfg(target_os = "macos")]
#[test]
fn async_mock_ipc_receipt_and_later_observation_use_the_same_joined_owner() {
    use std::time::{Duration, Instant};
    let mut fixture = super::port_pipe_tests::Fixture::start();
    let app = app(fixture.take_port());
    let call = |command, request| read(&app, "main", crate::LOCAL_TEST_ORIGIN, command, request);
    let status = || call("get_resident_status", json!({"protocolVersion":1})).unwrap();
    let poll = |revision| {
        let start = Instant::now();
        loop {
            let current = status();
            if current["revision"] == revision {
                return Ok(current);
            }
            if start.elapsed() > Duration::from_secs(3) {
                return Err("IPC observation timeout");
            }
            std::thread::sleep(Duration::from_millis(2));
        }
    };
    let mut on = intent();
    on["expectedRevision"] = 1.into();
    let before = status();
    let receipt = call("request_resident_intent", on.clone());
    let after = status();
    on["requestId"] = 2.into();
    let overlapping = call("request_resident_intent", on);
    fixture.release(0);
    let applied = poll(2);
    let off = call(
        "request_resident_intent",
        json!({"protocolVersion":1,"requestId":3,"expectedRevision":2,"intent":"off"}),
    );
    let after_off = status();
    fixture.release(1);
    let stopped = poll(3);
    let cleanup = fixture.finish();
    let ended = status();
    let legacy = call("get_shell_status", json!({"protocolVersion":1}));
    drop(app);
    drop(fixture); // original owner fully joined and test temp removed first
    assert_eq!(receipt.unwrap()["disposition"], "accepted");
    assert_eq!(overlapping.unwrap()["disposition"], "rejected");
    assert_eq!(off.unwrap()["disposition"], "accepted");
    for key in [
        "revision",
        "standby",
        "registration",
        "work",
        "cleanup",
        "transition",
        "canRequestOn",
        "canRequestOff",
    ] {
        assert_eq!(before[key], after[key], "receipt mutated {key}");
        assert_eq!(
            applied.as_ref().unwrap()[key],
            after_off[key],
            "OFF receipt mutated {key}"
        );
    }
    assert_eq!(applied.unwrap()["standby"], "on");
    assert_eq!(stopped.unwrap()["standby"], "off");
    assert_eq!(ended["validForMs"], 0);
    assert_eq!(ended["canRequestOn"], false);
    assert_eq!(ended["canRequestOff"], false);
    assert_eq!(cleanup, Some(crate::standby_lifecycle::Outcome::Confirmed));
    assert_eq!(
        legacy.unwrap(),
        json!({"protocolVersion":1,"integration":"not_connected","canStart":false,"canStop":false,"termination":"unconfirmed"})
    );
}

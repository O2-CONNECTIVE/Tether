use super::*;
use serde_json::{json, Value};

fn intent() -> Value {
    json!({"protocolVersion":1,"requestId":1,"expectedRevision":0,"intent":"on"})
}
#[test]
fn ordinary_port_is_unbound_and_returns_only_unavailable_receipts() {
    let port = ResidentPort::default();
    let status = serde_json::to_value(port.current()).unwrap();
    assert_eq!(
        status,
        if cfg!(target_os = "macos") {
            json!({"protocolVersion":1,"source":"resident-owner","state":"source-unavailable"})
        } else {
            json!({"protocolVersion":1,"source":"unsupported"})
        }
    );
    let receipt = port
        .request(serde_json::from_value(intent()).unwrap())
        .unwrap()
        .recv()
        .unwrap();
    assert_eq!(
        serde_json::to_value(receipt).unwrap(),
        json!({"protocolVersion":1,"requestId":1,"intent":"on","disposition":"unavailable"})
    );
    assert_eq!(serde_json::to_value(port.current()).unwrap(), status);
}
#[test]
fn unbound_path_still_validates_version_and_safe_integer_request_contract() {
    for (key, value, expected) in [
        ("protocolVersion", json!(2), "UNSUPPORTED_RESIDENT_VERSION"),
        ("requestId", json!(0), "INVALID_RESIDENT_INTENT"),
        (
            "requestId",
            json!(9_007_199_254_740_992_u64),
            "INVALID_RESIDENT_INTENT",
        ),
        (
            "expectedRevision",
            json!(9_007_199_254_740_992_u64),
            "INVALID_RESIDENT_INTENT",
        ),
    ] {
        let mut request = intent();
        request[key] = value;
        assert_eq!(
            ResidentPort::default()
                .request(serde_json::from_value(request).unwrap())
                .err(),
            Some(expected)
        );
    }
}
#[test]
fn serialized_request_has_no_binding_path_command_or_credential_override() {
    for (key, value) in [
        ("path", json!("/fake")),
        ("command", json!("shell")),
        ("credentials", json!("secret")),
        ("source", json!("authenticated")),
        ("requestId", json!("1")),
        ("intent", json!("restart")),
    ] {
        let mut request = intent();
        request[key] = value;
        assert!(serde_json::from_value::<IntentRequest>(request).is_err());
    }
}

#[test]
fn ordinary_shell_does_not_register_resident_or_binding_commands() {
    let app = crate::commands(tauri::test::mock_builder())
        .build(tauri::generate_context!(test = true))
        .unwrap();
    let window = tauri::WebviewWindowBuilder::new(&app, "main", Default::default())
        .build()
        .unwrap();
    for command in [
        "get_resident_status",
        "request_resident_intent",
        "bind_resident_source",
        "launch_resident",
        "set_resident_policy",
    ] {
        let request = if command == "get_resident_status" {
            json!({"protocolVersion":1})
        } else {
            intent()
        };
        let response = tauri::test::get_ipc_response(
            &window,
            tauri::webview::InvokeRequest {
                cmd: command.into(),
                callback: tauri::ipc::CallbackFn(0),
                error: tauri::ipc::CallbackFn(1),
                url: crate::LOCAL_TEST_ORIGIN.parse().unwrap(),
                body: tauri::ipc::InvokeBody::Json(json!({"request":request})),
                headers: Default::default(),
                invoke_key: tauri::test::INVOKE_KEY.into(),
            },
        );
        assert_eq!(
            response.err(),
            Some(json!(format!("Command {command} not found")))
        );
    }
}

use super::*;
use serde_json::{json, Value};

fn intent() -> Value {
    json!({"protocolVersion":1,"requestId":1,"expectedRevision":0,"intent":"on"})
}

#[cfg(all(target_os = "macos", resident_gui_fixture))]
#[test]
fn resident_gui_menu_receipt_reports_only_the_expected_off_once() {
    use model::{Disposition, Intent};

    for (disposition, label) in [
        (Disposition::Accepted, "menu-stop-receipt-accepted"),
        (Disposition::Rejected, "menu-stop-receipt-rejected"),
        (Disposition::Stale, "menu-stop-receipt-stale"),
        (Disposition::Unavailable, "menu-stop-receipt-unavailable"),
        (Disposition::Unknown, "menu-stop-receipt-unknown"),
    ] {
        let port = ResidentPort::default();
        let receipt = |request_id, intent| Receipt {
            protocol_version: 1,
            request_id,
            intent,
            disposition,
        };
        assert_eq!(
            port.take_menu_stop_receipt_label(&receipt(42, Intent::Off)),
            None
        );
        port.expect_menu_stop_receipt(42);
        assert_eq!(
            port.take_menu_stop_receipt_label(&receipt(42, Intent::On)),
            None
        );
        assert_eq!(
            port.take_menu_stop_receipt_label(&receipt(41, Intent::Off)),
            None
        );
        let off = receipt(42, Intent::Off);
        assert_eq!(port.take_menu_stop_receipt_label(&off), Some(label));
        assert_eq!(port.take_menu_stop_receipt_label(&off), None);
    }
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
fn ordinary_shell_resident_commands_remain_unbound_and_binding_is_absent() {
    let app = crate::commands(tauri::test::mock_builder())
        .build(tauri::generate_context!(test = true))
        .unwrap();
    assert_eq!(
        serde_json::to_value(&app.config().app.security.capabilities).unwrap(),
        json!(["shell-local"])
    );
    use tauri::Manager;
    assert!(app.try_state::<std::sync::Arc<ResidentState>>().is_some());
    assert!(app.try_state::<ResidentPort>().is_none());
    let window = tauri::WebviewWindowBuilder::new(&app, "main", Default::default())
        .build()
        .unwrap();
    for (command, request, expected) in [
        (
            "get_resident_status",
            json!({"protocolVersion":1}),
            serde_json::to_value(model::Status::unbound()).unwrap(),
        ),
        (
            "request_resident_intent",
            Value::String(intent().to_string()),
            json!({"protocolVersion":1,"requestId":1,"intent":"on","disposition":"unavailable"}),
        ),
    ] {
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
        )
        .unwrap()
        .deserialize::<Value>()
        .unwrap();
        assert_eq!(response, expected);
    }
    for command in [
        "bind_resident_source",
        "launch_resident",
        "set_resident_policy",
    ] {
        let request = intent();
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
        let reason = format!("{command} not allowed. Command not found");
        assert_eq!(response.err(), Some(json!(reason)));
    }
}

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
fn get_resident_status_after_stdout_eof_without_closed_is_expired_and_non_actionable() {
    use crate::mac_status::ContinuousClock;
    use std::{
        fs,
        path::PathBuf,
        process::Command,
        sync::atomic::{AtomicU64, Ordering},
        thread,
        time::{Duration, Instant},
    };

    let clock = ContinuousClock::new().unwrap();
    let now = clock.now_millis().unwrap();
    let frame = json!({
        "protocolVersion": 1,
        "type": "status",
        "sourceContinuousMs": now,
        "snapshot": {
            "protocolVersion": 1,
            "source": "resident-owner",
            "state": "source-observation",
            "revision": 1,
            "validForMs": 3000,
            "standby": "off",
            "registration": "disconnected",
            "caller": null,
            "work": "idle",
            "cleanup": "confirmed",
            "canRequestOn": true,
            "canRequestOff": false,
            "transition": null
        }
    });
    let deadline = frame["sourceContinuousMs"]
        .as_u64()
        .unwrap()
        .checked_add(frame["snapshot"]["validForMs"].as_u64().unwrap())
        .unwrap();
    struct OwnedDirectory(PathBuf);
    impl OwnedDirectory {
        fn new() -> Self {
            static NEXT: AtomicU64 = AtomicU64::new(0);
            let path = std::env::temp_dir().join(format!(
                "resident-ipc-eof-{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            fs::create_dir(&path).unwrap();
            Self(path)
        }
    }
    impl Drop for OwnedDirectory {
        fn drop(&mut self) {
            fs::remove_dir_all(&self.0).unwrap();
        }
    }
    struct ReleaseOnDrop(Vec<PathBuf>);
    impl Drop for ReleaseOnDrop {
        fn drop(&mut self) {
            for path in &self.0 {
                let _ = fs::write(path, []);
            }
        }
    }

    let directory = OwnedDirectory::new();
    let ready = directory.0.join("ready");
    let exit = directory.0.join("exit");
    let mut command = Command::new("/bin/sh");
    command
        .arg("-c")
        .arg("wait_for_file() { count=0; while [ ! -e \"$1\" ] && [ \"$count\" -lt 300 ]; do /bin/sleep 0.01; count=$((count + 1)); done; [ -e \"$1\" ]; }; wait_for_file \"$2\" || exit 8; printf '%s\\n' \"$1\"; wait_for_file \"$3\" || exit 9; exit 7")
        .arg("resident-eof-fixture")
        .arg(frame.to_string())
        .arg(ready.to_string_lossy().into_owned())
        .arg(exit.to_string_lossy().into_owned())
        .env_clear();
    let (mut owner, source) =
        crate::resident_port::duplex::owner::Supervisor::fixture(command).unwrap();
    let _release_on_drop = ReleaseOnDrop(vec![ready.clone(), exit.clone()]);
    fs::write(&ready, []).unwrap();
    let app = app(ResidentPort::bound(source.unwrap()).unwrap());
    let status = || {
        read(
            &app,
            "main",
            crate::LOCAL_TEST_ORIGIN,
            "get_resident_status",
            json!({"protocolVersion":1}),
        )
        .unwrap()
    };
    let start = Instant::now();
    let before = loop {
        let current = status();
        if current["state"] == "source-observation" {
            break current;
        }
        assert!(
            start.elapsed() < Duration::from_secs(3),
            "sidecar status was not observed"
        );
        thread::sleep(Duration::from_millis(2));
    };
    assert_eq!(before["revision"], 1);
    assert!(before["validForMs"].as_u64().unwrap() > 1000);
    assert_eq!(before["canRequestOn"], true);
    assert_eq!(before["canRequestOff"], false);

    let remaining = Duration::from_millis(before["validForMs"].as_u64().unwrap());
    let after_exit = Instant::now();
    fs::write(&exit, []).unwrap();
    let ended = loop {
        if let Some(result) = owner.poll_join() {
            break result;
        }
        assert!(
            after_exit.elapsed() < remaining,
            "status expired by its original deadline"
        );
        thread::sleep(Duration::from_millis(2));
    };
    assert!(
        matches!(ended, Ok(crate::resident_port::duplex::owner::End::Reaped(status)) if status.code() == Some(7)),
        "child result: {ended:?}"
    );
    assert!(
        after_exit.elapsed() < remaining,
        "sidecar ended after the original status deadline"
    );
    let expired = status();
    let observed_at = clock.now_millis().unwrap();
    assert!(
        observed_at < deadline,
        "environment was too slow to verify EOF expiry before the status deadline"
    );
    assert_eq!(expired["revision"], before["revision"]);
    assert_eq!(expired["validForMs"], 0);
    assert_eq!(expired["canRequestOn"], false);
    assert_eq!(expired["canRequestOff"], false);
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

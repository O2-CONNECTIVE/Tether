//! Disposable CI only: real WebView/IPC/owned pipes, real Go Standby with synthetic loopback trust only.
//! This does not activate the Mac27 sealed native launcher or enroll a host.
use crate::resident_gui_probe as probe;
use crate::resident_port::{duplex::owner::Supervisor, ResidentPort, ResidentState};
use crate::standby_lifecycle::{Outcome, StandbyOwner};
use std::fs::OpenOptions;
use std::io::Write;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, AtomicU8, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};
use tauri::{Listener, Manager};

const TEST: &str = "TestCIResidentGUIWiring";
pub(crate) fn event(stage: &'static str) {
    eprintln!("GUI_FIXTURE_EVENT={stage}");
}
fn advance(state: &mut u8, payload: &str) -> &'static str {
    let step = if payload.len() <= 64 {
        match payload {
            "\"initializer-started\"" => Some((0, 1, "initializer-started")),
            "\"on-applied\"" => Some((1, 2, "on-applied")),
            "\"off-applied\"" => Some((2, 3, "off-applied")),
            "\"menu-on-applied\"" => Some((3, 4, "menu-on-applied")),
            "\"menu-off-applied\"" => Some((4, 5, "menu-off-applied")),
            "\"passed\"" => Some((5, 6, "driver-passed")),
            _ => None,
        }
    } else {
        None
    };
    if let Some((previous, next, label)) = step {
        if *state == previous {
            *state = next;
            return label;
        }
    }
    *state = u8::MAX;
    "driver-failed"
}
// Only closed enums are projected; payloads, exceptions and runtime values never leave CI.
fn failure_class(payload: &str) -> (&'static str, &'static str) {
    let Some(value) = payload
        .strip_prefix("\"failed:")
        .and_then(|v| v.strip_suffix('"'))
    else {
        return ("event", "sequence");
    };
    let Some((phase, reason)) = value.split_once(':') else {
        return ("event", "sequence");
    };
    let phase = match phase {
        "init" => "init",
        "on" => "on",
        "auth" => "auth",
        "reopen" => "reopen",
        "off" => "off",
        "menu-stop" => "menu-stop",
        "complete" => "complete",
        _ => return ("event", "sequence"),
    };
    let reason = match reason {
        "identity" => "identity",
        "receipt" => "receipt",
        "transition" => "transition",
        "retry-budget" => "retry-budget",
        "phase-timeout" => "phase-timeout",
        "auth-timeout" => "auth-timeout",
        "bridge-timeout" => "bridge-timeout",
        "window" => "window",
        "unexpected" => "unexpected",
        _ => return ("event", "sequence"),
    };
    (phase, reason)
}
fn failure_event(phase: &'static str, reason: &'static str) {
    eprintln!("GUI_FIXTURE_FAILURE={phase}:{reason}");
}
fn menu_stop_diagnostic(payload: &str) -> Option<&'static str> {
    match payload {
        "\"stop-received\"" => Some("menu-stop-signal-received"),
        _ => None,
    }
}
fn hex(value: &str, count: usize) -> bool {
    value.len() == count
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
fn retain() -> ! {
    loop {
        thread::park_timeout(Duration::from_secs(3600));
    }
}
fn wait_until(mut check: impl FnMut() -> bool) -> bool {
    let until = Instant::now() + Duration::from_secs(5);
    while Instant::now() < until {
        if check() {
            return true;
        }
        thread::sleep(Duration::from_millis(20));
    }
    false
}
fn reopen_main(app: &tauri::AppHandle, window: &tauri::WebviewWindow) -> bool {
    if crate::mac_menu_bar::show_main_window(app).is_err()
        || !wait_until(|| matches!(window.is_visible(), Ok(true)))
    {
        return false;
    }
    event("menu-bar-window-reopened");
    window
        .eval("window.dispatchEvent(new Event('tether-fixture-window-reopened'))")
        .is_ok()
}

fn stop_hidden_standby(
    app: &tauri::AppHandle,
    window: &tauri::WebviewWindow,
) -> Result<(), &'static str> {
    let owner = app.state::<Arc<ResidentState>>();
    let read = || serde_json::to_value(owner.port().current()).unwrap_or_default();
    let on = read();
    let (Some(off_id), Some(on_revision)) = (
        on["transition"]["requestId"]
            .as_u64()
            .and_then(|id| id.checked_add(1)),
        on["revision"].as_u64(),
    ) else {
        return Err("on-precondition");
    };
    if on["standby"] != "on"
        || on["registration"] != "authenticated"
        || on["transition"]["outcome"] != "applied"
    {
        return Err("on-precondition");
    }
    owner.port().expect_menu_stop_receipt(off_id);
    crate::mac_menu_bar::menu_event(
        app,
        tauri::menu::MenuEvent {
            id: crate::mac_menu_bar::STOP_STANDBY.into(),
        },
    )
    .map_err(|_| "dispatch")?;
    event("menu-stop-dispatched");
    if !wait_until(|| {
        let off = read();
        off["revision"]
            .as_u64()
            .is_some_and(|revision| revision > on_revision)
            && off["standby"] == "off"
            && off["cleanup"] == "confirmed"
            && off["canRequestOn"] == true
            && off["transition"]["requestId"].as_u64() == Some(off_id)
            && off["transition"]["intent"] == "off"
            && off["transition"]["outcome"] == "applied"
    }) {
        return Err("off-proof-timeout");
    }
    if !matches!(window.is_visible(), Ok(false)) {
        return Err("visibility");
    }
    event("menu-stop-applied-while-hidden");
    Ok(())
}

fn check_menu_bar(
    app: &tauri::AppHandle,
    webview_hidden: &AtomicBool,
    stop: bool,
) -> Result<(), &'static str> {
    let window = app.get_webview_window("main").ok_or("window")?;
    webview_hidden.store(false, Ordering::SeqCst);
    if window.close().is_err()
        || !wait_until(|| {
            matches!(window.is_visible(), Ok(false)) && webview_hidden.load(Ordering::SeqCst)
        })
    {
        return Err("window");
    }
    event("menu-bar-window-hidden");
    if stop {
        stop_hidden_standby(app, &window)?;
    }
    if !reopen_main(app, &window) {
        return Err(if stop { "reopen" } else { "window" });
    }
    Ok(())
}
fn report(path: &PathBuf, invocation: &str, passed: bool) {
    let value = serde_json::json!({
        "test": TEST, "invocation": invocation,
        "outcome": if passed {"passed"} else {"failed-unknown"},
        "cleanup": if passed {"confirmed"} else {"unconfirmed"},
        "scope": "real-webview-ipc-go-standby-loopback",
        "native27_launch": "not-tested", "product_connection": "not-tested",
        "source": std::env::var("CI_GUI_SOURCE_SHA").unwrap_or_default(),
        "build_utc": std::env::var("CI_GUI_BUILD_UTC").unwrap_or_default(),
        "binary_sha256": std::env::var("CI_GUI_BINARY_SHA256").unwrap_or_default(),
        "producer_sha256": std::env::var("CI_GUI_PRODUCER_SHA256").unwrap_or_default(),
        "label": "カレントの検証版"
    });
    let bytes = serde_json::to_vec(&value).unwrap();
    let result = OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
        .and_then(|mut f| {
            f.write_all(&bytes)?;
            f.sync_all()
        });
    if result.is_err() {
        retain();
    }
}

pub(super) fn run() {
    let expected = [
        ("CI_ACTUAL_PRODUCER", "1"),
        ("RUNNER_ENVIRONMENT", "github-hosted"),
        ("RUNNER_OS", "macOS"),
        ("RUNNER_ARCH", "ARM64"),
    ];
    if expected
        .iter()
        .any(|(key, value)| std::env::var(key).as_deref() != Ok(*value))
    {
        std::process::exit(2);
    }
    let invocation = std::env::var("CI_PRODUCER_INVOCATION").unwrap_or_default();
    let path = PathBuf::from(std::env::var("CI_PRODUCER_RESULT").unwrap_or_default());
    if !hex(&invocation, 32)
        || !path.is_absolute()
        || path.symlink_metadata().is_ok()
        || !hex(&std::env::var("CI_GUI_SOURCE_SHA").unwrap_or_default(), 40)
        || !hex(
            &std::env::var("CI_GUI_BINARY_SHA256").unwrap_or_default(),
            64,
        )
    {
        std::process::exit(2);
    }
    probe::identity(event);
    let binary = PathBuf::from(std::env::var("CI_GUI_PRODUCER_BINARY").unwrap_or_default());
    if !binary.is_absolute() || !binary.is_file() {
        std::process::exit(2);
    }
    let mut command = std::process::Command::new(binary);
    command
        .args([
            "-test.run=^TestCIResidentGUIProducer$",
            "-test.count=1",
            "-test.timeout=0",
        ])
        .env_clear();
    for (key, value) in expected {
        command.env(key, value);
    }
    let (producer, source) = match Supervisor::gui_fixture(command) {
        Ok(parts) => parts,
        Err(_) => {
            report(&path, &invocation, false);
            retain();
        }
    };
    event("producer-started");
    let mut owner = match StandbyOwner::start(producer) {
        Ok(owner) => owner,
        Err(_) => {
            report(&path, &invocation, false);
            retain();
        }
    };
    let port = match source.and_then(ResidentPort::bound) {
        Ok(port) => port,
        Err(_) => {
            report(&path, &invocation, false);
            std::hint::black_box(&owner);
            retain();
        }
    };
    event("source-bound");
    let verdict = Arc::new(AtomicU8::new(0));
    let observed = verdict.clone();
    let window_action = Arc::new(AtomicU8::new(0));
    let received_on = window_action.clone();
    let webview_hidden = Arc::new(AtomicBool::new(false));
    let received_hidden = webview_hidden.clone();
    let builder = crate::commands_with_ports(
        tauri::Builder::default().on_window_event(crate::mac_menu_bar::window_event),
        crate::ShellRuntime::default(),
        crate::StatusPresentation::default(),
        port,
    );
    let context = tauri::generate_context!("tests/resident-gui/tauri.conf.json");
    event(match context.assets().get(&"index.html".into()) {
        Some(bytes) if !bytes.is_empty() => "embedded-index-present",
        Some(_) => "embedded-index-empty",
        None => "embedded-index-missing",
    });
    let app = builder
        .setup(move |app| {
            let sequence = std::sync::Mutex::new(0_u8);
            let result = observed.clone();
            app.listen("resident-gui-menu-stop", |message| {
                if let Some(label) = menu_stop_diagnostic(message.payload()) {
                    event(label);
                }
            });
            // Only dedicated CI context local/main can emit. Events are not
            // authority: same pipe ON/OFF and original owner proof remain required.
            app.listen("resident-gui-fixture", move |message| {
                let Ok(mut state) = sequence.lock() else {
                    result.store(2, Ordering::SeqCst);
                    event("driver-failed");
                    failure_event("event", "lock");
                    return;
                };
                if message.payload() == "\"window-hidden\""
                    && matches!(*state, 2 | 4)
                    && !received_hidden.swap(true, Ordering::SeqCst)
                {
                    return;
                }
                let stage = advance(&mut state, message.payload());
                event(stage);
                match stage {
                    "on-applied" => received_on.store(1, Ordering::SeqCst),
                    "menu-on-applied" => received_on.store(2, Ordering::SeqCst),
                    _ => {}
                }
                if stage == "driver-failed" {
                    let (phase, reason) = failure_class(message.payload());
                    failure_event(phase, reason);
                }
                if *state == 6 {
                    result.store(1, Ordering::SeqCst);
                }
                if *state == u8::MAX {
                    result.store(2, Ordering::SeqCst);
                }
            });

            let window = tauri::WebviewWindowBuilder::new(
                app,
                "main",
                tauri::WebviewUrl::App("index.html".into()),
            )
            .title("Tether — カレントの検証版（合成CI）")
            .initialization_script("window.__tetherFixtureMenuStop = true")
            .initialization_script(include_str!("../tests/resident-gui/drive.js"))
            .on_navigation(|url| {
                let allowed = probe::navigation_allowed(url);
                event(if allowed {
                    "navigation-root-allowed"
                } else {
                    "navigation-denied"
                });
                allowed
            })
            .on_web_resource_request(|request, response| {
                if probe::is_index(request.uri()) {
                    event(probe::response(
                        response.status().as_u16(),
                        response
                            .headers()
                            .get("content-type")
                            .and_then(|v| v.to_str().ok()),
                        !response.body().is_empty(),
                    ));
                }
            })
            .on_page_load(|_window, payload| match payload.event() {
                tauri::webview::PageLoadEvent::Started => event("page-started"),
                tauri::webview::PageLoadEvent::Finished => event("page-finished"),
            })
            .build()?;
            crate::mac_menu_bar::install(app.handle())?;
            window.show()?;
            event("webview-created");
            Ok(())
        })
        .build(context);
    let app = match app {
        Ok(app) => app,
        Err(_) => {
            report(&path, &invocation, false);
            std::hint::black_box(&owner);
            retain();
        }
    };
    event("app-built");
    let gate = owner.gate.clone();
    let wake_gate = gate.clone();
    let wake = app.handle().clone();
    gate.set_wake(Arc::new(move || {
        let finished = wake_gate.clone();
        let handle = wake.clone();
        // Never join the monitor from its own completion callback.
        let _ = wake.run_on_main_thread(move || {
            if finished.finish_requested() {
                handle.exit(0);
            }
        });
    }));
    let request = app.handle().clone();
    let observed = verdict.clone();
    let monitor = thread::spawn(move || {
        let until = Instant::now() + Duration::from_secs(60);
        while observed.load(Ordering::SeqCst) == 0 && Instant::now() < until {
            let action = window_action.swap(0, Ordering::SeqCst);
            if action != 0 {
                let stop = action == 2;
                if let Err(reason) = check_menu_bar(&request, &webview_hidden, stop) {
                    failure_event(if stop { "menu-stop" } else { "reopen" }, reason);
                    observed.store(2, Ordering::SeqCst);
                    break;
                }
            }
            thread::sleep(Duration::from_millis(20));
        }
        event("monitor-exit-request");
        // Even a failed GUI assertion requests the SAME owner close; never kill.
        request.exit(0);
    });
    event("event-loop-start");
    let exit_code = app.run_return(move |_handle, run_event| {
        if let tauri::RunEvent::ExitRequested { api, .. } = run_event {
            event("exit-requested");
            if !gate.request_exit() {
                event("exit-held");
                api.prevent_exit();
            }
        }
    });
    event("event-loop-returned");
    let monitor_joined = monitor.join().is_ok();
    owner.join(); // exact producer IO/Wait AND original outer monitor
    event("owner-joined");
    let passed = exit_code == 0
        && monitor_joined
        && verdict.load(Ordering::SeqCst) == 1
        && owner.gate.outcome() == Some(Outcome::Confirmed);
    report(&path, &invocation, passed);
    if !passed {
        std::hint::black_box(&owner);
        retain();
    }
}

#[cfg(test)]
mod tests {
    use super::{advance, failure_class, menu_stop_diagnostic};

    #[test]
    fn menu_stop_diagnostics_project_only_fixed_labels() {
        assert_eq!(
            menu_stop_diagnostic("\"stop-received\""),
            Some("menu-stop-signal-received")
        );
        for input in [
            "\"passed\"",
            "\"stop-received:42\"",
            "\"receipt-accepted\"",
            "\"receipt-stale\\n\"",
            "receipt-stale",
            "{}",
        ] {
            assert_eq!(menu_stop_diagnostic(input), None);
        }
    }
    #[test]
    fn fixed_failure_classes_never_accept_payload_values_or_restore_success() {
        for phase in [
            "init",
            "on",
            "auth",
            "reopen",
            "off",
            "menu-stop",
            "complete",
        ] {
            for reason in [
                "identity",
                "receipt",
                "transition",
                "retry-budget",
                "phase-timeout",
                "auth-timeout",
                "bridge-timeout",
                "window",
                "unexpected",
            ] {
                let payload = format!("\"failed:{phase}:{reason}\"");
                assert_eq!(failure_class(&payload), (phase, reason));
                let mut state = 1;
                assert_eq!(advance(&mut state, &payload), "driver-failed");
                assert_eq!(state, u8::MAX);
                advance(&mut state, "\"on-applied\"");
                assert_eq!(state, u8::MAX);
            }
        }
        for payload in [
            "\"failed:on:https://secret.invalid\"",
            "\"failed:private:identity\"",
            "{}",
            "\"passed\"",
            "\"failed:on:identity:extra\"",
            "\"failed:on:identity\\n\"",
        ] {
            assert_eq!(failure_class(payload), ("event", "sequence"));
        }
    }
    #[test]
    fn fixture_verdict_requires_order_and_rejects_duplicate_or_oversized_events() {
        let events = [
            "\"initializer-started\"",
            "\"on-applied\"",
            "\"off-applied\"",
            "\"menu-on-applied\"",
            "\"menu-off-applied\"",
            "\"passed\"",
        ];
        let mut state = 0;
        for message in events {
            advance(&mut state, message);
        }
        assert_eq!(state, 6);
        advance(&mut state, events[5]);
        assert_eq!(state, u8::MAX);
        let mut without_menu_stop = 3;
        assert_eq!(
            advance(&mut without_menu_stop, "\"passed\""),
            "driver-failed"
        );
        for bad in ["\"passed\"", "\"failed\"", "{}", &"x".repeat(65)] {
            let mut state = 0;
            advance(&mut state, bad);
            for message in events {
                advance(&mut state, message);
            }
            assert_eq!(state, u8::MAX);
        }
    }
}

//! Disposable CI only: real WebView/IPC/owned pipes, synthetic peer only.
//! This does not activate the Mac27 sealed native launcher or enroll a host.
use crate::resident_gui_probe as probe;
use crate::resident_port::{duplex::owner::Supervisor, ResidentPort};
use crate::standby_lifecycle::{Outcome, StandbyOwner};
use std::fs::OpenOptions;
use std::io::Write;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU8, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};
use tauri::Listener;

const TEST: &str = "TestCIResidentGUIWiring";
fn event(stage: &'static str) {
    eprintln!("GUI_FIXTURE_EVENT={stage}");
}
fn advance(state: &mut u8, payload: &str) -> &'static str {
    let step = if payload.len() <= 64 {
        match payload {
            "\"initializer-started\"" => Some((0, 1, "initializer-started")),
            "\"on-applied\"" => Some((1, 2, "on-applied")),
            "\"off-applied\"" => Some((2, 3, "off-applied")),
            "\"passed\"" => Some((3, 4, "driver-passed")),
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
    *state = 5;
    "driver-failed"
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
fn report(path: &PathBuf, invocation: &str, passed: bool) {
    let value = serde_json::json!({
        "test": TEST, "invocation": invocation,
        "outcome": if passed {"passed"} else {"failed-unknown"},
        "cleanup": if passed {"confirmed"} else {"unconfirmed"},
        "scope": "real-webview-ipc-synthetic-pipe",
        "native27_launch": "not-tested", "product_connection": "not-tested",
        "source": std::env::var("CI_GUI_SOURCE_SHA").unwrap_or_default(),
        "build_utc": std::env::var("CI_GUI_BUILD_UTC").unwrap_or_default(),
        "binary_sha256": std::env::var("CI_GUI_BINARY_SHA256").unwrap_or_default(),
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
    let mut command = std::process::Command::new("/usr/bin/python3");
    command
        .args([
            "-I",
            "-u",
            "-c",
            include_str!("../tests/resident-gui/producer.py"),
        ])
        .env_clear();
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
    let builder = crate::commands_with_ports(
        tauri::Builder::default(),
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
            // Only dedicated CI context local/main can emit. Events are not
            // authority: same pipe ON/OFF and original owner proof remain required.
            app.listen("resident-gui-fixture", move |message| {
                let Ok(mut state) = sequence.lock() else {
                    result.store(2, Ordering::SeqCst);
                    event("driver-failed");
                    return;
                };
                event(advance(&mut state, message.payload()));
                if *state == 4 {
                    result.store(1, Ordering::SeqCst);
                }
                if *state == 5 {
                    result.store(2, Ordering::SeqCst);
                }
            });

            let window = tauri::WebviewWindowBuilder::new(
                app,
                "main",
                tauri::WebviewUrl::App("index.html".into()),
            )
            .title("Tether — カレントの検証版（合成CI）")
            .initialization_script(include_str!("../tests/resident-gui/drive.js"))
            .on_navigation(move |url| {
                if url.scheme() != "tauri"
                    || url.host_str() != Some("localhost")
                    || !url.username().is_empty()
                    || url.password().is_some()
                    || url.port().is_some()
                    || url.query().is_some()
                    || url.fragment().is_some()
                {
                    event("navigation-denied");
                    return false;
                }
                let allowed = matches!(url.path(), "/" | "/index.html");
                if allowed {
                    event("navigation-root-allowed");
                }
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
    use super::advance;
    #[test]
    fn fixture_verdict_requires_order_and_rejects_duplicate_or_oversized_events() {
        let events = [
            "\"initializer-started\"",
            "\"on-applied\"",
            "\"off-applied\"",
            "\"passed\"",
        ];
        let mut state = 0;
        for message in events {
            advance(&mut state, message);
        }
        assert_eq!(state, 4);
        advance(&mut state, events[3]);
        assert_eq!(state, 5);
        for bad in ["\"passed\"", "\"failed\"", "{}", &"x".repeat(65)] {
            let mut state = 0;
            advance(&mut state, bad);
            for message in events {
                advance(&mut state, message);
            }
            assert_eq!(state, 5);
        }
    }
}

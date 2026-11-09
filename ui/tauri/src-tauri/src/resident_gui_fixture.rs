//! Disposable CI only: real WebView/IPC/owned pipes, synthetic peer only.
//! This does not activate the Mac27 sealed native launcher or enroll a host.
use crate::resident_port::{duplex::owner::Supervisor, ResidentPort};
use crate::standby_lifecycle::{Outcome, StandbyOwner};
use std::fs::OpenOptions;
use std::io::Write;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU8, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};
use tauri::Manager;

const TEST: &str = "TestCIResidentGUIWiring";
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
    let verdict = Arc::new(AtomicU8::new(0));
    let observed = verdict.clone();
    let builder = crate::commands_with_ports(
        tauri::Builder::default(),
        crate::ShellRuntime::default(),
        crate::StatusPresentation::default(),
        port,
    );
    let app = builder
        .setup(move |app| {
            let status = observed.clone();
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
                    return false;
                }
                match url.path() {
                    "/__resident_fixture_pass" => {
                        status.store(1, Ordering::SeqCst);
                        false
                    }
                    "/__resident_fixture_fail" => {
                        status.store(2, Ordering::SeqCst);
                        false
                    }
                    "/" | "/index.html" => true,
                    _ => false,
                }
            })
            .build()?;
            window.show()?;
            Ok(())
        })
        .build(tauri::generate_context!());
    let app = match app {
        Ok(app) => app,
        Err(_) => {
            report(&path, &invocation, false);
            std::hint::black_box(&owner);
            retain();
        }
    };
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
        // Even a failed GUI assertion requests the SAME owner close; never kill.
        request.exit(0);
    });
    app.run(move |_handle, event| {
        if let tauri::RunEvent::ExitRequested { api, .. } = event {
            if !gate.request_exit() {
                api.prevent_exit();
            }
        }
    });
    let monitor_joined = monitor.join().is_ok();
    owner.join(); // exact producer IO/Wait AND original outer monitor
    let passed = monitor_joined
        && verdict.load(Ordering::SeqCst) == 1
        && owner.gate.outcome() == Some(Outcome::Confirmed);
    report(&path, &invocation, passed);
    if !passed {
        std::hint::black_box(&owner);
        retain();
    }
}

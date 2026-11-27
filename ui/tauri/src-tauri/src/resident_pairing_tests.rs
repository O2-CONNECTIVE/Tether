use super::*;
use std::sync::{atomic::AtomicUsize, Mutex};

struct FakeRunner {
    output: Mutex<HelperOutput>,
    calls: AtomicUsize,
}
impl FakeRunner {
    fn new(output: HelperOutput) -> Self {
        Self {
            output: Mutex::new(output),
            calls: AtomicUsize::new(0),
        }
    }
}
impl HelperRunner for FakeRunner {
    fn run(&self) -> HelperOutput {
        self.calls.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        self.output.lock().unwrap().clone()
    }
}

fn exited(stdout: &str) -> HelperOutput {
    HelperOutput::Exited {
        status: 0,
        stdout: stdout.as_bytes().to_vec(),
    }
}

#[test]
fn accepts_only_one_complete_v1_result_line() {
    // Why: the helper stdout is an IPC boundary and malformed data must never become a success.
    assert_eq!(
        decode_helper_output(exited("{\"protocolVersion\":1,\"result\":\"succeeded\"}\n")),
        PairingResult::Succeeded
    );
    for stdout in [
        "{\"protocolVersion\":1,\"result\":\"succeeded\"}",
        "{\"protocolVersion\":1,\"result\":\"succeeded\",\"extra\":0}\n",
        "{\"protocolVersion\":1,\"result\":\"succeeded\"}\n{}\n",
        "{\"protocolVersion\":2,\"result\":\"succeeded\"}\n",
        "{\"protocolVersion\":1,\"result\":\"future\"}\n",
    ] {
        assert_eq!(
            decode_helper_output(exited(stdout)),
            PairingResult::Unavailable,
            "{stdout}"
        );
    }
}

#[test]
fn rejects_whitespace_outside_the_json_object() {
    // Why: serde_json skips outer whitespace, but helper stdout requires exact object bytes and one LF.
    for stdout in [
        " {\"protocolVersion\":1,\"result\":\"succeeded\"}\n",
        "\t{\"protocolVersion\":1,\"result\":\"succeeded\"}\n",
        "{\"protocolVersion\":1,\"result\":\"succeeded\"} \n",
        "{\"protocolVersion\":1,\"result\":\"succeeded\"}\n\n",
        "{\"protocolVersion\":1,\"result\":\"succeeded\"}",
    ] {
        assert_eq!(
            decode_helper_output(exited(stdout)),
            PairingResult::Unavailable,
            "{stdout:?}"
        );
    }
}

#[test]
fn native_helper_output_maps_codes_and_rejects_an_oversized_length() {
    // Why: FFI status codes and lengths must be translated safely without invoking native code in Rust tests.
    assert_eq!(
        native_helper_output(1, 3, 7, b"abcx".to_vec()),
        HelperOutput::Exited {
            status: 7,
            stdout: b"abc".to_vec()
        }
    );
    assert_eq!(
        native_helper_output(2, 0, 0, Vec::new()),
        HelperOutput::TimedOut
    );
    for (code, length) in [(0, 0), (3, 0), (1, 4097)] {
        assert_eq!(
            native_helper_output(code, length, 0, vec![0; 4096]),
            HelperOutput::Unavailable
        );
    }
}

#[test]
fn nonzero_exit_timeout_and_oversize_are_unavailable() {
    // Why: a valid-looking line cannot override process failure, timeout, or the 4 KiB cap.
    let valid = b"{\"protocolVersion\":1,\"result\":\"succeeded\"}\n".to_vec();
    for output in [
        HelperOutput::Exited {
            status: 1,
            stdout: valid,
        },
        HelperOutput::TimedOut,
        HelperOutput::Exited {
            status: 0,
            stdout: vec![b'x'; 4097],
        },
        HelperOutput::Unavailable,
    ] {
        assert_eq!(decode_helper_output(output), PairingResult::Unavailable);
    }
}

#[test]
fn a_busy_pairing_does_not_run_the_helper() {
    // Why: duplicate clicks and other windows must not start a second registration operation.
    let runner = Arc::new(FakeRunner::new(exited("{}\n")));
    let manager = PairingManager::new(runner.clone());
    manager
        .busy
        .store(true, std::sync::atomic::Ordering::Release);
    assert_eq!(manager.run(), PairingResult::Busy);
    assert_eq!(runner.calls.load(std::sync::atomic::Ordering::SeqCst), 0);
}

#[test]
fn pairing_success_returns_the_helper_result_without_submitting_resident_intents() {
    // Why: the WebView owns request IDs; pairing must not create a separate native OFF request.
    let runner = Arc::new(FakeRunner::new(exited(
        "{\"protocolVersion\":1,\"result\":\"succeeded\"}\n",
    )));
    let manager = PairingManager::new(runner.clone());
    let result = manager.run();
    assert_eq!(result, PairingResult::Succeeded);
    assert_eq!(runner.calls.load(std::sync::atomic::Ordering::SeqCst), 1);
}

#[test]
fn async_request_releases_its_permit_after_the_blocking_worker_completes() {
    // Why: the production async path must move its permit into spawn_blocking and return it afterward.
    let runner = Arc::new(FakeRunner::new(exited(
        "{\"protocolVersion\":1,\"result\":\"succeeded\"}\n",
    )));
    let manager = PairingManager::new(runner.clone());
    for _ in 0..2 {
        let response = tauri::async_runtime::block_on(manager.request());
        assert_eq!(response.result, PairingResult::Succeeded);
    }
    assert_eq!(runner.calls.load(std::sync::atomic::Ordering::SeqCst), 2);
}

struct PanicOnceRunner(AtomicUsize);
impl HelperRunner for PanicOnceRunner {
    fn run(&self) -> HelperOutput {
        if self.0.fetch_add(1, std::sync::atomic::Ordering::SeqCst) == 0 {
            panic!("fixture panic");
        }
        exited("{\"protocolVersion\":1,\"result\":\"succeeded\"}\n")
    }
}

#[test]
fn async_request_releases_its_permit_when_the_runner_panics() {
    // Why: unwinding in spawn_blocking must not leave later registration requests permanently busy.
    let runner = Arc::new(PanicOnceRunner(AtomicUsize::new(0)));
    let manager = PairingManager::new(runner.clone());
    let first = tauri::async_runtime::block_on(manager.request());
    assert_eq!(first.result, PairingResult::Unavailable);
    let second = tauri::async_runtime::block_on(manager.request());
    assert_eq!(second.result, PairingResult::Succeeded);
    assert_eq!(runner.0.load(std::sync::atomic::Ordering::SeqCst), 2);
}

#[test]
fn webview_response_contains_only_the_result_kind() {
    // Why: helper bytes and diagnostics are private to the native/Rust boundary.
    assert_eq!(
        serde_json::to_value(PairingResponse {
            result: PairingResult::Rejected
        })
        .unwrap(),
        serde_json::json!({"result":"rejected"})
    );
}

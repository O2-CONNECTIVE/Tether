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

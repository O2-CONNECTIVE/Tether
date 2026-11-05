//! Actual owned stdin/stdout pipes to harmless shell fixtures, NOT auth proof.
use super::{
    owner::{End, Supervisor},
    test_frames::{intent_request as request, status_line as status},
};
use crate::mac_status::ContinuousClock;
use serde_json::json;
use std::process::Command;
use std::thread;
use std::time::{Duration, Instant};
fn now() -> u64 {
    ContinuousClock::new().unwrap().now_millis().unwrap()
}
fn child(script: &str) -> (Supervisor, super::owner::BoundDuplex) {
    let mut command = Command::new("/bin/sh");
    command.arg("-c").arg(script).env_clear();
    let (owner, source) = Supervisor::fixture(command).unwrap();
    (owner, source.unwrap())
}
fn reap(owner: &mut Supervisor) -> End {
    let start = Instant::now();
    loop {
        if let Some(result) = owner.poll_join() {
            return result.unwrap();
        }
        assert!(start.elapsed() < Duration::from_secs(10));
        thread::sleep(Duration::from_millis(5));
    }
}
fn receipt(id: u64) -> String {
    json!({"protocolVersion":1,"type":"receipt","receipt":{
    "protocolVersion":1,"requestId":id,"intent":"on","disposition":"accepted"}})
    .to_string()
}
#[test]
fn same_owned_child_delivers_receipt_and_buffered_terminal_status() {
    let first = status(1, now(), json!(null));
    let terminal = status(
        2,
        now(),
        json!({"requestId":1,"intent":"on","outcome":"rejected"}),
    );
    let script = format!(
        "printf '%s\\n' '{}'; IFS= read -r line; printf '%s\\n' '{}' '{}'; exit 7",
        first,
        receipt(1),
        terminal
    );
    let (mut owner, source) = child(&script);
    assert_eq!(
        source
            .first_with_timeout(Duration::from_secs(1))
            .unwrap()
            .revision,
        1
    );
    let response = source
        .request(request(1, 1))
        .unwrap()
        .recv_timeout(Duration::from_secs(2))
        .unwrap();
    assert_eq!(
        serde_json::to_value(response).unwrap()["disposition"],
        "accepted"
    );
    assert!(matches!(reap(&mut owner),End::Reaped(status) if status.code()==Some(7)));
    let final_status = source.current().unwrap();
    assert_eq!(final_status.revision, 2);
    assert_eq!(final_status.valid_for_ms, 0);
    assert!(!final_status.can_request_on); // EOF/reaping does not claim cleanup.
}
#[test]
fn wrong_receipt_is_unknown_and_not_retried() {
    let script = format!(
        "printf '%s\\n' '{}'; IFS= read -r line; printf '%s\\n' '{}'; IFS= read -r extra; exit 0",
        status(1, now(), json!(null)),
        receipt(2)
    );
    let (mut owner, source) = child(&script);
    source.first_with_timeout(Duration::from_secs(1)).unwrap();
    let response = source
        .request(request(1, 1))
        .unwrap()
        .recv_timeout(Duration::from_secs(2))
        .unwrap();
    assert_eq!(
        serde_json::to_value(response).unwrap()["disposition"],
        "unknown"
    );
    reap(&mut owner);
    assert_eq!(source.current().unwrap().valid_for_ms, 0);
    let response = source
        .request(request(2, 1))
        .unwrap()
        .recv_timeout(Duration::from_secs(1))
        .unwrap();
    assert_ne!(
        serde_json::to_value(response).unwrap()["disposition"],
        "accepted"
    );
}
#[test]
fn dropping_viewer_does_not_close_pipe_or_signal_child() {
    let script = format!(
        "printf '%s\\n' '{}'; sleep 0.2; exit 9",
        status(1, now(), json!(null))
    );
    let (mut owner, source) = child(&script);
    source.first_with_timeout(Duration::from_secs(1)).unwrap();
    drop(source);
    assert!(owner.poll_join().is_none());
    assert!(matches!(reap(&mut owner),End::Reaped(status) if status.code()==Some(9)));
}
#[test]
fn initial_snapshot_timeout_retains_owner_until_child_reaped() {
    let (mut owner, source) = child("IFS= read -r line; exit 3");
    assert!(source
        .first_with_timeout(Duration::from_millis(20))
        .is_err());
    assert!(matches!(reap(&mut owner),End::Reaped(status) if status.code()==Some(3)));
    assert!(source.current().is_none());
}
#[test]
fn written_request_without_receipt_times_out_unknown_and_closes_only_owned_pipe() {
    let script = format!(
        "printf '%s\\n' '{}'; IFS= read -r line; IFS= read -r second; exit 4",
        status(1, now(), json!(null))
    );
    let (mut owner, source) = child(&script);
    source.first_with_timeout(Duration::from_secs(1)).unwrap();
    let mut before = source.current().unwrap();
    let waiting = source.request(request(1, 1)).unwrap();
    let mut after = source.current().unwrap();
    // Same producer revision/fingerprint, including capabilities, survives
    // local submission. Only elapsed freshness may change (frontend contract).
    before.valid_for_ms = 0;
    after.valid_for_ms = 0;
    assert_eq!(before, after);
    assert!(waiting.try_recv().is_err()); // Successful write is NOT acceptance.
    let other = source
        .request(request(2, 1))
        .unwrap()
        .recv_timeout(Duration::from_secs(1))
        .unwrap();
    assert_eq!(
        serde_json::to_value(other).unwrap()["disposition"],
        "rejected"
    );
    let response = waiting.recv_timeout(Duration::from_secs(5)).unwrap();
    assert_eq!(
        serde_json::to_value(response).unwrap()["disposition"],
        "unknown"
    );
    assert!(matches!(reap(&mut owner),End::Reaped(status) if status.code()==Some(4)));
}
#[test]
fn truncated_or_oversized_stdout_never_creates_snapshot() {
    for output in ["{\"type\":\"status\"}".to_owned(), "x".repeat(4097)] {
        let (mut owner, source) = child(&format!("printf '%s' '{output}'; exit 0"));
        assert!(source.first_with_timeout(Duration::from_secs(1)).is_err());
        reap(&mut owner);
        assert!(source.current().is_none());
    }
}

#[test]
fn attachment_error_partial_capture_and_panic_retain_exact_child() {
    use super::owner::fixture_launcher::Attachment;
    for fault in [Attachment::Before, Attachment::Partial, Attachment::Panic] {
        let mut command = Command::new("/bin/sh");
        command
            .arg("-c")
            .arg("IFS= read -r line; exit 6")
            .env_clear();
        let (mut owner, source) = Supervisor::fixture_with(command, fault).unwrap();
        assert!(source.is_err());
        assert!(matches!(reap(&mut owner),End::Reaped(status) if status.code()==Some(6)));
    }
}

#[test]
fn explicit_transport_close_not_viewer_close_keeps_same_child_until_reaped() {
    let script = format!(
        "printf '%s\\n' '{}'; IFS= read -r line; printf '%s\\n' '{}'; exit 8",
        status(1, now(), json!(null)),
        status(2, now(), json!(null))
    );
    let (mut owner, source) = child(&script);
    source.first_with_timeout(Duration::from_secs(1)).unwrap();
    owner.request_close_transport();
    assert!(matches!(reap(&mut owner),End::Reaped(status) if status.code()==Some(8)));
    assert!(!owner.cleanup_confirmed()); // A normal status is not post-Close proof.
    assert_eq!(source.current().unwrap().valid_for_ms, 0);
}

#[test]
fn partial_stdout_deadline_is_bounded_without_silently_renewing() {
    let script = format!(
        "printf '%s\\n' '{}'; printf '{{'; IFS= read -r line; exit 5",
        status(1, now(), json!(null))
    );
    let (mut owner, source) = child(&script);
    source.first_with_timeout(Duration::from_secs(1)).unwrap();
    assert!(matches!(reap(&mut owner),End::Reaped(status) if status.code()==Some(5)));
    assert_eq!(source.current().unwrap().valid_for_ms, 0);
}

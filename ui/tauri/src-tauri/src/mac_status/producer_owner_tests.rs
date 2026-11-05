//! Harmless owned fixture CHILDREN, not the genuine producer or live authentication.
use super::*;
use std::time::Instant;
fn command(script: &str) -> Command {
    let mut command = Command::new("/bin/sh");
    command
        .args(["-c", script])
        .env_clear()
        .stdin(Stdio::null())
        .stderr(Stdio::null());
    command
}
fn finish(owner: &mut ProducerSupervisor) -> ExitStatus {
    let end = Instant::now() + Duration::from_secs(3);
    loop {
        if let Some(result) = owner.poll_join() {
            return match result.unwrap() {
                ProducerEnd::Reaped(exit) => exit,
                ProducerEnd::NotSpawned => panic!("fixture not spawned"),
            };
        }
        assert!(Instant::now() < end, "fixture supervisor timed out");
        thread::sleep(Duration::from_millis(5));
    }
}
#[test]
fn child_stdout_is_bound_and_terminal_bytes_drain_before_reap_report() {
    let now = ContinuousClock::new().unwrap().now_seconds().unwrap();
    let line = format!(
        r#"{{"version":1,"source_continuous_ms":{},"seq":1,"phase":"closed","reason":"none","grant_remaining_ms":0,"starting_workers":0,"running_workers":0,"stopping_workers":0,"cleanup":"confirmed","terminal_cleanup_unknown":false,"terminal":true}}"#,
        (now * 1000.0) as u64
    );
    let script = format!("printf '%s\\n' '{}'", line);
    let (mut owner, source) = ProducerSupervisor::start(command(&script)).unwrap();
    let source = source.unwrap();
    assert!(finish(&mut owner).success());
    let (snapshot, now) = source.observation();
    assert_eq!(snapshot.frame.as_ref().unwrap().seq, 1);
    assert!(snapshot.source_closed());
    assert!(snapshot.terminal_observed());
    assert!(!snapshot.fresh(now));
}
#[test]
fn viewer_drop_does_not_kill_child_or_discard_supervisor() {
    let (mut owner, source) = ProducerSupervisor::start(command("sleep 0.2; exit 7")).unwrap();
    drop(source.unwrap());
    assert!(owner.poll_join().is_none());
    assert_eq!(finish(&mut owner).code(), Some(7));
}
#[test]
fn post_spawn_attachment_failure_keeps_and_reaps_exact_child() {
    let (mut owner, source) = ProducerSupervisor::start_with(command("sleep 0.2; exit 9"), |_| {
        Err(io::Error::other("injected attach failure"))
    })
    .unwrap();
    assert!(source.is_err());
    assert!(owner.poll_join().is_none());
    assert_eq!(finish(&mut owner).code(), Some(9));
}
#[test]
fn nonexistent_fixture_executable_reports_spawn_failure_with_retained_owner() {
    let (mut owner, source) =
        ProducerSupervisor::start(Command::new("/no-such-mac-test-fixture")).unwrap();
    assert!(source.is_err());
    let end = Instant::now() + Duration::from_secs(3);
    loop {
        if let Some(result) = owner.poll_join() {
            assert!(matches!(result.unwrap(), ProducerEnd::NotSpawned));
            break;
        }
        assert!(Instant::now() < end);
        thread::sleep(Duration::from_millis(5));
    }
}

#[test]
fn attachment_panic_keeps_exact_child_for_reaping() {
    let (mut owner, source) = ProducerSupervisor::start_with(command("sleep 0.2; exit 11"), |_| {
        panic!("synthetic attachment panic")
    })
    .unwrap();
    assert!(source.is_err());
    assert_eq!(finish(&mut owner).code(), Some(11));
}

#[test]
fn producer_drop_guard_blocks_unknown_before_resident_owner_exists() {
    let result = std::panic::catch_unwind(|| {
        wait_known_owner(false, || panic!("observed unknown-owner retention"));
    });
    assert!(result.is_err());
    wait_known_owner(true, || panic!("known outcome should not wait"));
}

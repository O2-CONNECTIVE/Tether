use super::super::{exchange::Shared, test_frames::status_line};
use super::Pump;
use crate::mac_status::ContinuousClock;
use serde_json::{json, Value};
use std::io::Cursor;

fn status(revision: u64, source: u64, cleanup: &str, can_on: bool, can_off: bool) -> String {
    let mut frame: Value =
        serde_json::from_str(&status_line(revision, source, json!(null))).unwrap();
    frame["snapshot"]["cleanup"] = json!(cleanup);
    frame["snapshot"]["canRequestOn"] = json!(can_on);
    frame["snapshot"]["canRequestOff"] = json!(can_off);
    frame.to_string()
}

fn feed(
    pump: &mut Pump<Cursor<Vec<u8>>>,
    shared: &mut Shared,
    line: &str,
    now: u64,
) -> Result<(), ()> {
    pump.consume(shared, format!("{line}\n").as_bytes(), now)
}

fn closing() -> (Pump<Cursor<Vec<u8>>>, Shared) {
    let mut pump = Pump::with_io(Cursor::new(Vec::new()));
    let mut shared = Shared::default();
    feed(
        &mut pump,
        &mut shared,
        &status(2, 100, "confirmed", true, false),
        100,
    )
    .unwrap();
    shared.close_control(100);
    shared.closure.half_closed();
    (pump, shared)
}

#[test]
fn post_close_status_does_not_clear_a_recorded_cleanup_unknown() {
    // Unknown cleanup never reverts: a later status must not hide it from a
    // view that first reads after that status.
    let (mut pump, mut shared) = closing();
    feed(
        &mut pump,
        &mut shared,
        &status(3, 101, "unknown", false, false),
        101,
    )
    .unwrap();
    feed(
        &mut pump,
        &mut shared,
        &status(4, 102, "confirmed", false, false),
        102,
    )
    .unwrap();
    let observed = serde_json::to_value(shared.current(102).unwrap()).unwrap();
    assert_eq!(observed["cleanup"], "unknown");
    assert_eq!(observed["revision"], 3);
}

#[test]
fn post_close_revision_rollback_is_not_recorded_or_a_close_failure() {
    let (mut pump, mut shared) = closing();
    feed(
        &mut pump,
        &mut shared,
        &status(1, 101, "confirmed", true, false),
        101,
    )
    .unwrap();
    assert_eq!(shared.current(101).unwrap().revision, 2);
    feed(
        &mut pump,
        &mut shared,
        &json!({"protocolVersion":1,"type":"closed","sourceContinuousMs":102,"outcome":"confirmed"}).to_string(),
        102,
    )
    .unwrap();
    shared.ended();
    assert!(shared.closure.confirmed());
}

#[test]
fn post_close_same_revision_content_change_is_not_recorded_or_a_close_failure() {
    let (mut pump, mut shared) = closing();
    feed(
        &mut pump,
        &mut shared,
        &status(2, 101, "unknown", false, false),
        101,
    )
    .unwrap();
    assert_eq!(
        serde_json::to_value(shared.current(101).unwrap()).unwrap()["cleanup"],
        "confirmed"
    );
    feed(
        &mut pump,
        &mut shared,
        &json!({"protocolVersion":1,"type":"closed","sourceContinuousMs":102,"outcome":"confirmed"}).to_string(),
        102,
    )
    .unwrap();
    shared.ended();
    assert!(shared.closure.confirmed());
}

#[test]
fn actionable_status_after_close_stays_non_actionable() {
    let (mut pump, mut shared) = closing();
    feed(
        &mut pump,
        &mut shared,
        &status(3, 101, "confirmed", true, false),
        101,
    )
    .unwrap();

    let observed = serde_json::to_value(shared.current(101).unwrap()).unwrap();
    assert_eq!(observed["revision"], 3);
    assert_eq!(observed["validForMs"], 0);
    assert_eq!(observed["canRequestOn"], false);
    assert_eq!(observed["canRequestOff"], false);
}

#[test]
fn expired_post_close_status_does_not_block_confirmed_terminal_or_eof() {
    let clock = ContinuousClock::new().unwrap();
    let mut now = clock.now_millis().unwrap();
    while now <= 5_000 {
        std::thread::sleep(std::time::Duration::from_millis(1));
        now = clock.now_millis().unwrap();
    }

    let close_at = now - 5_000;
    let mut setup = Pump::with_io(Cursor::new(Vec::new()));
    let mut shared = Shared::default();
    feed(
        &mut setup,
        &mut shared,
        &status(2, close_at - 1, "confirmed", true, false),
        close_at - 1,
    )
    .unwrap();
    shared.close_control(close_at);
    shared.closure.half_closed();

    let expired = status(3, close_at + 1_000, "confirmed", true, false);
    let terminal_source = clock.now_millis().unwrap();
    let terminal = json!({
        "protocolVersion":1,
        "type":"closed",
        "sourceContinuousMs":terminal_source,
        "outcome":"confirmed"
    });
    let input = format!("{expired}\n{}\n", terminal);
    let mut pump = Pump::with_io(Cursor::new(input.into_bytes()));

    assert!(!pump.step_io(&mut shared, &clock, now).unwrap());
    assert!(pump
        .step_io(&mut shared, &clock, clock.now_millis().unwrap())
        .unwrap());
    assert!(shared.closure.confirmed());
    assert_eq!(
        shared
            .current(clock.now_millis().unwrap())
            .unwrap()
            .revision,
        3
    );
}

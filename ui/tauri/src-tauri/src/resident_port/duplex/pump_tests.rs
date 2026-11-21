use super::super::{exchange::Shared, test_frames::status_line};
use super::Pump;
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
fn post_close_revision_rollback_is_rejected() {
    let (mut pump, mut shared) = closing();
    assert!(feed(
        &mut pump,
        &mut shared,
        &status(1, 101, "confirmed", true, false),
        101,
    )
    .is_err());
}

#[test]
fn post_close_same_revision_content_change_is_rejected() {
    let (mut pump, mut shared) = closing();
    assert!(feed(
        &mut pump,
        &mut shared,
        &status(2, 101, "unknown", false, false),
        101,
    )
    .is_err());
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

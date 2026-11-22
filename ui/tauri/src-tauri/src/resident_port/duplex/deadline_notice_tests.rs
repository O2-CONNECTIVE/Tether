use super::{exchange::Shared, test_frames, tracking::Tracking};
use crate::resident_port::model::{Disposition, Intent};
use serde_json::{json, Value};

const ORIGINAL_END: u64 = 1_900_000_000_000;

fn record_status(tracking: &mut Tracking, frame: &Value, now: u64) {
    assert!(tracking
        .event(test_frames::event(frame), now)
        .unwrap()
        .is_none());
}

fn begin(tracking: &mut Tracking, intent: Intent, id: u64, revision: u64, now: u64) {
    let mut request = test_frames::intent_request(id, revision);
    request.intent = intent;
    if intent == Intent::Extend {
        request.standby_hours = Some(4);
    }
    tracking.begin(&request, now).unwrap();
}

fn record_receipt(
    tracking: &mut Tracking,
    intent: Intent,
    id: u64,
    disposition: Disposition,
    now: u64,
) {
    let mut receipt = test_frames::receipt();
    receipt["receipt"]["intent"] = json!(intent);
    receipt["receipt"]["requestId"] = json!(id);
    receipt["receipt"]["disposition"] = json!(disposition);
    let received = tracking
        .event(test_frames::event(&receipt), now)
        .unwrap()
        .unwrap();
    assert_eq!(received.disposition, disposition);
}

fn on_status(revision: u64, source: u64, until: u64, soon: bool) -> Value {
    let mut frame = test_frames::status(source);
    let snapshot = &mut frame["snapshot"];
    snapshot["revision"] = json!(revision);
    snapshot["standby"] = json!("on");
    snapshot["registration"] = json!("authenticated");
    snapshot["canRequestOn"] = json!(false);
    snapshot["canRequestOff"] = json!(true);
    snapshot["transition"] = json!({"requestId":1,"intent":"on","outcome":"applied"});
    snapshot["standbyUntil"] = json!(until);
    snapshot["standbyEndingSoon"] = json!(soon);
    frame
}

fn deadline(tracking: &mut Tracking, now: u64) -> Option<(u64, u64, bool)> {
    tracking
        .standby_deadline(now)
        .map(|d| (d.request_id, d.unix_ms, d.ending_soon))
}

fn active_cycle() -> Tracking {
    let mut tracking = Tracking::default();
    record_status(&mut tracking, &on_status(2, 101, ORIGINAL_END, true), 101);
    assert_eq!(deadline(&mut tracking, 101), Some((1, ORIGINAL_END, true)));
    tracking
}

#[test]
fn accepted_on_needs_fresh_applied_status_to_establish_the_deadline() {
    let mut tracking = Tracking::default();
    assert_eq!(deadline(&mut tracking, 100), None);
    record_status(&mut tracking, &test_frames::status(100), 100);
    begin(&mut tracking, Intent::On, 1, 1, 100);
    record_receipt(&mut tracking, Intent::On, 1, Disposition::Accepted, 100);
    assert_eq!(deadline(&mut tracking, 100), None);

    record_status(&mut tracking, &on_status(2, 101, ORIGINAL_END, false), 101);
    assert_eq!(deadline(&mut tracking, 101), Some((1, ORIGINAL_END, false)));
}

#[test]
fn clock_projection_and_stale_off_overlay_keep_the_same_deadline_id() {
    let mut tracking = active_cycle();
    let adjusted_end = ORIGINAL_END + 1_500;
    record_status(&mut tracking, &on_status(3, 102, adjusted_end, true), 102);
    assert_eq!(deadline(&mut tracking, 102), Some((1, adjusted_end, true)));

    begin(&mut tracking, Intent::Off, 2, 3, 102);
    let mut refused = on_status(4, 103, adjusted_end, true);
    refused["snapshot"]["transition"] = json!({"requestId":2,"intent":"off","outcome":"rejected"});
    // Go publishes the resolved refusal before sending the stale receipt.
    record_status(&mut tracking, &refused, 103);
    assert_eq!(deadline(&mut tracking, 103), None);
    record_receipt(&mut tracking, Intent::Off, 2, Disposition::Stale, 103);
    assert_eq!(deadline(&mut tracking, 103), Some((1, adjusted_end, true)));
}

#[test]
fn accepted_extend_waits_for_a_strictly_newer_status() {
    let mut tracking = active_cycle();
    begin(&mut tracking, Intent::Extend, 2, 2, 102);
    assert_eq!(deadline(&mut tracking, 102), None);
    record_receipt(&mut tracking, Intent::Extend, 2, Disposition::Accepted, 102);
    assert_eq!(deadline(&mut tracking, 102), None);

    let mut old_status = on_status(2, 103, ORIGINAL_END, true);
    old_status["snapshot"]["validForMs"] = json!(2998);
    record_status(&mut tracking, &old_status, 103);
    assert_eq!(deadline(&mut tracking, 103), None);

    let extended_end = ORIGINAL_END + 4 * 60 * 60 * 1000;
    record_status(&mut tracking, &on_status(3, 104, extended_end, false), 104);
    assert_eq!(deadline(&mut tracking, 104), Some((2, extended_end, false)));
}

#[test]
fn stale_or_rejected_extend_cannot_replace_the_deadline_id() {
    for disposition in [Disposition::Stale, Disposition::Rejected] {
        let mut tracking = active_cycle();
        begin(&mut tracking, Intent::Extend, 2, 2, 102);
        record_receipt(&mut tracking, Intent::Extend, 2, disposition, 102);
        assert_eq!(deadline(&mut tracking, 102), Some((1, ORIGINAL_END, true)));

        let adjusted_end = ORIGINAL_END + 1_500;
        record_status(&mut tracking, &on_status(3, 103, adjusted_end, true), 103);
        assert_eq!(deadline(&mut tracking, 103), Some((1, adjusted_end, true)));
    }
}

#[test]
fn unknown_extend_receipt_suppresses_the_last_applied_on() {
    let mut tracking = active_cycle();
    begin(&mut tracking, Intent::Extend, 2, 2, 102);
    record_receipt(&mut tracking, Intent::Extend, 2, Disposition::Unknown, 102);
    assert_eq!(deadline(&mut tracking, 102), None);

    // Until the producer stops after reporting unknown, status may still be
    // in flight. Another fresh-looking projection cannot restore the notice.
    record_status(&mut tracking, &on_status(3, 103, ORIGINAL_END, true), 103);
    assert_eq!(deadline(&mut tracking, 103), None);
}

#[test]
fn unresolved_off_suppresses_the_last_applied_on() {
    let mut tracking = active_cycle();
    begin(&mut tracking, Intent::Off, 2, 2, 102);
    assert_eq!(deadline(&mut tracking, 102), None);
    record_receipt(&mut tracking, Intent::Off, 2, Disposition::Accepted, 102);
    assert_eq!(deadline(&mut tracking, 102), None);
}

#[test]
fn old_expired_and_unknown_statuses_suppress_the_deadline() {
    let mut old = active_cycle();
    assert_eq!(deadline(&mut old, 3_101), None);

    let mut expired = active_cycle();
    let mut expired_status = test_frames::status(102);
    expired_status["snapshot"]["revision"] = json!(3);
    expired_status["snapshot"]["standbyExpired"] = json!(true);
    record_status(&mut expired, &expired_status, 102);
    assert_eq!(deadline(&mut expired, 102), None);

    let mut unknown = active_cycle();
    let mut unknown_status = test_frames::status(102);
    let snapshot = &mut unknown_status["snapshot"];
    snapshot["revision"] = json!(3);
    snapshot["standby"] = json!("unknown");
    snapshot["registration"] = json!("unknown");
    snapshot["work"] = json!("unknown");
    snapshot["cleanup"] = json!("unknown");
    snapshot["canRequestOn"] = json!(false);
    snapshot["transition"] = json!({"requestId":1,"intent":"on","outcome":"unknown"});
    record_status(&mut unknown, &unknown_status, 102);
    assert_eq!(deadline(&mut unknown, 102), None);
}

#[test]
fn close_suppresses_even_the_last_fresh_deadline() {
    let mut shared = Shared {
        tracking: active_cycle(),
        ..Shared::default()
    };
    assert!(shared.standby_deadline(101).is_some());
    shared.close_control(102);
    assert!(shared.standby_deadline(102).is_none());
}

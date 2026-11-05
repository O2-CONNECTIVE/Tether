use super::{closure::Closure, test_frames::*};
use serde_json::json;

fn closing() -> Closure {
    let mut c = Closure::default();
    c.request(100);
    c.half_closed();
    c
}

#[test]
fn wire_confirmation_requires_request_half_close_terminal_and_eof() {
    assert!(!Closure::default().confirmed());
    for requested in [false, true] {
        let mut c = Closure::default();
        if requested {
            c.request(100);
        } else {
            c.half_closed();
        }
        assert!(c.event(event(&terminal(120, "confirmed")), 120).is_err());
        c.eof();
        assert!(!c.confirmed());
    }
    let mut c = closing();
    c.event(event(&terminal(120, "confirmed")), 120).unwrap();
    assert!(!c.confirmed());
    c.eof();
    assert!(c.confirmed()); // Wire evidence only; no child/join proof exists here.
}

#[test]
fn ordinary_off_status_and_accepted_off_receipt_are_not_close_proof() {
    let mut c = closing();
    c.event(event(&status(100)), 100).unwrap();
    c.event(event(&receipt()), 100).unwrap();
    c.eof();
    assert!(!c.confirmed());
}

#[test]
fn eof_seals_the_stream_before_any_late_terminal_record() {
    let mut c = closing();
    c.eof();
    assert!(c.event(event(&terminal(120, "confirmed")), 120).is_err());
    assert!(!c.confirmed());
}

#[test]
fn unknown_and_explicit_fault_never_recover_with_later_success() {
    let mut c = closing();
    c.event(event(&terminal(120, "unknown")), 120).unwrap();
    assert!(c.event(event(&terminal(121, "confirmed")), 121).is_err());
    c.request(122);
    c.half_closed();
    c.eof();
    assert!(!c.confirmed());
    let mut c = closing();
    c.event(event(&terminal(120, "confirmed")), 120).unwrap();
    c.invalidate();
    c.eof();
    assert!(!c.confirmed());
    assert!(c.tick(121).is_err());
}

#[test]
fn duplicate_or_any_following_frame_invalidates_terminal() {
    for following in [terminal(121, "confirmed"), status(121), receipt()] {
        let mut c = closing();
        c.event(event(&terminal(120, "confirmed")), 120).unwrap();
        assert!(c.event(event(&following), 121).is_err());
        c.eof();
        assert!(!c.confirmed());
    }
}

#[test]
fn startup_rejection_is_not_the_post_close_proof() {
    let mut c = closing();
    assert!(c.event(event(&rejection()), 100).is_err());
    c.eof();
    assert!(!c.confirmed());
}

#[test]
fn terminal_source_must_follow_request_and_be_fresh_without_future_clock() {
    for (source, now) in [(99, 100), (101, 100), (100, 3100)] {
        let mut c = closing();
        assert!(c.event(event(&terminal(source, "confirmed")), now).is_err());
        assert!(c.event(event(&terminal(now, "confirmed")), now).is_err());
        c.eof();
        assert!(!c.confirmed());
    }
    let mut c = closing();
    c.event(event(&terminal(100, "confirmed")), 3099).unwrap();
    c.eof();
    assert!(c.confirmed());
}

#[test]
fn old_ordinary_status_does_not_apply_a_three_second_shutdown_timeout() {
    let mut c = closing();
    c.event(event(&status(0)), 75_000).unwrap();
    c.event(event(&terminal(75_000, "confirmed")), 75_000)
        .unwrap();
    c.eof();
    assert!(c.confirmed());
}

#[test]
fn source_and_native_clock_regressions_latch_failure() {
    for (source, now) in [(150, 200), (200, 199)] {
        let mut c = closing();
        c.event(event(&status(200)), 200).unwrap();
        assert!(c.event(event(&terminal(source, "confirmed")), now).is_err());
        c.eof();
        assert!(!c.confirmed());
    }
    let mut c = closing();
    c.event(event(&terminal(200, "confirmed")), 200).unwrap();
    assert!(c.tick(199).is_err());
    c.eof();
    assert!(!c.confirmed());
}

#[test]
fn seeded_prior_source_is_retained_and_request_cannot_renew_shutdown() {
    let mut c = Closure::default();
    c.seed_source(Some(200));
    c.request(200);
    c.seed_source(None);
    c.half_closed();
    assert!(c.event(event(&terminal(199, "confirmed")), 200).is_err());
    c.eof();
    assert!(!c.confirmed());
    let mut c = closing();
    c.request(150);
    c.event(event(&terminal(110, "confirmed")), 150).unwrap();
    c.eof();
    assert!(c.confirmed());
}

#[test]
fn malformed_status_invariant_invalidates_close_for_the_lifetime() {
    let mut value = status(100);
    value["snapshot"]["cleanup"] = json!("unknown");
    let mut c = closing();
    assert!(c.event(event(&value), 100).is_err());
    assert!(c.event(event(&terminal(120, "confirmed")), 120).is_err());
    c.eof();
    assert!(!c.confirmed());
}

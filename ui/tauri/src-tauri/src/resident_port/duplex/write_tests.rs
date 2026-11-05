//! Injected syscall-result tests, distinct from actual pipe fixture tests.
use super::*;
use crate::resident_port::duplex::{
    test_frames::{intent_request as request, status_line as status},
    wire::Event,
};
use serde_json::json;
use std::io::ErrorKind;
#[test]
fn short_zero_failed_or_interrupted_write_is_unknown_without_remainder() {
    for result in [
        Ok(1),
        Ok(0),
        Err(std::io::Error::from(ErrorKind::BrokenPipe)),
        Err(std::io::Error::from(ErrorKind::WouldBlock)),
        Err(std::io::Error::from(ErrorKind::Interrupted)),
    ] {
        let mut shared = Shared::default();
        shared
            .tracking
            .event(
                Event::parse(status(1, 100, json!(null)).as_bytes()).unwrap(),
                100,
            )
            .unwrap();
        let response = shared.submit(request(1, 1), 100);
        assert!(shared.output.as_ref().unwrap().len() > 1);
        assert!(shared.written(result).is_err());
        assert!(shared.output.is_none());
        assert!(shared.tracking.failed);
        assert_eq!(
            serde_json::to_value(response.try_recv().unwrap()).unwrap()["disposition"],
            "unknown"
        );
        assert_eq!(shared.current(100).unwrap().valid_for_ms, 0);
    }
}
#[test]
fn complete_write_does_not_fabricate_receipt() {
    let mut shared = Shared::default();
    shared
        .tracking
        .event(
            Event::parse(status(1, 100, json!(null)).as_bytes()).unwrap(),
            100,
        )
        .unwrap();
    let response = shared.submit(request(1, 1), 100);
    let len = shared.output.as_ref().unwrap().len();
    shared.written(Ok(len)).unwrap();
    assert!(shared.output.is_none());
    assert!(response.try_recv().is_err());
    shared.fault();
    assert_eq!(
        serde_json::to_value(response.try_recv().unwrap()).unwrap()["disposition"],
        "unknown"
    );
}

#[test]
fn deliberate_quit_seals_admission_and_discards_queued_frame_as_unknown() {
    let mut shared = Shared::default();
    shared
        .tracking
        .event(
            Event::parse(status(1, 100, json!(null)).as_bytes()).unwrap(),
            100,
        )
        .unwrap();
    let response = shared.submit(request(1, 1), 100);
    shared.close_control(100);
    assert!(shared.output.is_none());
    assert_eq!(
        serde_json::to_value(response.try_recv().unwrap()).unwrap()["disposition"],
        "unknown"
    );
    let later = shared.submit(request(2, 1), 100);
    assert_eq!(
        serde_json::to_value(later.try_recv().unwrap()).unwrap()["disposition"],
        "rejected"
    );
    assert_eq!(shared.current(100).unwrap().valid_for_ms, 0);
}

#[test]
fn quit_cannot_reset_preclose_clock_or_source_highwater() {
    for (quit, source) in [(150, 200), (300, 150)] {
        let mut shared = Shared::default();
        shared
            .tracking
            .event(
                Event::parse(status(1, 200, json!(null)).as_bytes()).unwrap(),
                200,
            )
            .unwrap();
        shared.close_control(quit);
        shared.closure.half_closed();
        let frame = Event::parse(status(2, source, json!(null)).as_bytes()).unwrap();
        assert!(shared.closure.event(frame, 300).is_err());
        shared.closure.eof();
        assert!(!shared.closure.confirmed());
    }
}

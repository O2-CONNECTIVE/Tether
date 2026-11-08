//! Fixed untrusted self-child; no policy, producer or real external connection.
use super::test_frames;
use crate::mac_status::ContinuousClock;
use serde_json::{json, Value};
use std::io::{BufRead, Write};
fn now() -> u64 {
    ContinuousClock::new().unwrap().now_millis().unwrap()
}
fn emit(value: Value) {
    let mut out = std::io::stdout().lock();
    writeln!(out, "{value}").unwrap();
    out.flush().unwrap();
}
pub(crate) fn run(arguments: &[String]) {
    assert_eq!(arguments.len(), 3);
    assert_eq!(arguments[0], "active-child");
    let descriptions: Vec<_> = arguments[2].split(',').collect();
    assert_eq!(descriptions.len(), 4);
    for end in descriptions {
        assert_eq!(
            end.split(':').filter(|v| v.parse::<u64>().is_ok()).count(),
            3
        );
    }
    match arguments[1].as_str() {
        "early" => {
            println!("{{not-json");
            return;
        }
        "exit-two" => {
            emit(
                json!({"protocolVersion":1,"type":"startupRejected","sourceContinuousMs":now(),"code":"policy-rejected"}),
            );
            std::process::exit(2);
        }
        "signal" => {
            emit(test_frames::status(now()));
            unsafe extern "C" {
                fn raise(signal: i32) -> i32;
            }
            // SAFETY: fixed own child signals only itself, never owner cleanup.
            unsafe {
                raise(15);
            }
            std::process::exit(89);
        }
        "echo" => (),
        _ => std::process::exit(78),
    }
    emit(test_frames::status(now()));
    let mut id = 0;
    let mut late_receipt = None;
    for line in std::io::stdin().lock().lines() {
        let line = line.unwrap();
        assert!(line.len() <= 4095);
        let frame: Value = serde_json::from_str(&line).unwrap();
        assert_eq!(frame["protocolVersion"], 1);
        assert_eq!(frame["type"], "intent");
        assert_eq!(frame["request"]["intent"], "on");
        assert_eq!(frame["request"]["expectedRevision"], 1);
        id = frame["request"]["requestId"].as_u64().unwrap();
        assert!((1..=5).contains(&id));
        let mut receipt = test_frames::receipt();
        receipt["receipt"]["requestId"] = json!(id);
        receipt["receipt"]["intent"] = json!("on");
        if id == 5 {
            let mut pending = test_frames::status(now());
            pending["snapshot"]["revision"] = json!(2);
            pending["snapshot"]["canRequestOn"] = json!(false);
            pending["snapshot"]["transition"] =
                json!({"requestId":5,"intent":"on","outcome":"pending"});
            emit(pending); // proves read/write completed, not acceptance/application
            late_receipt = Some(receipt);
        } else {
            emit(receipt);
        }
    }
    if let Some(receipt) = late_receipt {
        emit(receipt); // only AFTER the original control EOF
    }
    if id != 3 {
        emit(test_frames::terminal(
            now(),
            if id == 2 { "unknown" } else { "confirmed" },
        ));
    }
    if id == 4 {
        println!("{{bad-after-close");
    }
}

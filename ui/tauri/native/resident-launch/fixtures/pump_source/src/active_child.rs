//! Fixed untrusted self-child; no policy, producer or real external connection.
use super::test_frames;
use crate::mac_status::ContinuousClock;
use serde_json::{Value, json};
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
        "finite-held" => {
            await_control_eof();
            finite_output();
            return;
        }
        "finite" => {
            finite_output();
            return;
        }
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

// Fixed self-owned escape makes the red collectable without parent kill.
fn finite_output() {
    unsafe extern "C" {
        fn fcntl(fd: i32, command: i32, ...) -> i32;
    }
    // SAFETY: only this fixed child stdout, Darwin F_GETFL/F_SETFL/O_NONBLOCK.
    unsafe {
        let flags = fcntl(1, 3);
        assert!(flags >= 0 && fcntl(1, 4, flags | 4) == 0);
    }
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
    let mut bytes = vec![b'x'; 1024 * 1024];
    bytes[..10].copy_from_slice(b"{not-json\n");
    let mut offset = 0;
    let mut out = std::io::stdout().lock();
    while offset < bytes.len() {
        match out.write(&bytes[offset..]) {
            Ok(0) => std::process::exit(87),
            Ok(n) => offset += n,
            Err(e)
                if matches!(
                    e.kind(),
                    std::io::ErrorKind::WouldBlock | std::io::ErrorKind::Interrupted
                ) => {}
            Err(_) => std::process::exit(87),
        }
        if std::time::Instant::now() >= deadline {
            std::process::exit(88);
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
}

// Synchronize on the fixture gate, not scheduler timing. The timer is failure
// escape only, so a broken parent can still collect this exact child.
fn await_control_eof() {
    use std::io::Read;
    unsafe extern "C" {
        fn fcntl(fd: i32, command: i32, ...) -> i32;
    }
    // SAFETY: fixed self-child stdin only, Darwin F_GETFL/F_SETFL/O_NONBLOCK.
    unsafe {
        let flags = fcntl(0, 3);
        assert!(flags >= 0 && fcntl(0, 4, flags | 4) == 0);
    }
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
    loop {
        match std::io::stdin().read(&mut [0]) {
            Ok(0) => return,
            Ok(_) => std::process::exit(87),
            Err(e)
                if matches!(
                    e.kind(),
                    std::io::ErrorKind::WouldBlock | std::io::ErrorKind::Interrupted
                ) => {}
            Err(_) => std::process::exit(87),
        }
        if std::time::Instant::now() >= deadline {
            std::process::exit(88);
        }
        std::thread::yield_now();
    }
}

//! Placeholders are replaced with exact source paths and actual patched Wry closure bodies.
use std::{sync::mpsc, thread};
use webview2_com::{windows, Result, Error};
const E_POINTER: i32 = -14;
const E_UNEXPECTED: i32 = -15;
#[path = "@RECORDER@"] mod delivery;
#[unsafe(no_mangle)] pub extern "C" fn rescue_host_wry_stage(_: u8) {}

fn main() {
    let case = std::env::args().nth(1).unwrap();
    let p = delivery::Probe::memory(); assert!(p.register());
    for kind in 1..=2 {
        let scope = webview2_com::rescue_host_delivery::Scope::begin(kind);
        let __delivery_token = if case == "wrong-token" { 99 } else { scope.token() };
        let (tx, rx) = mpsc::channel::<Result<u8>>();
        let callback: Box<dyn FnOnce(windows::core::Result<()>, Option<u8>) -> windows::core::Result<()> + Send> =
            if kind == 1 { Box::new(@ENV_CALLBACK@) } else { Box::new(@CONTROLLER_CALLBACK@) };
        if case == "disconnected" {
            drop(rx); let _api = scope.api();
            assert_eq!(callback(Ok(()), Some(7)), Err(windows::core::Error(E_UNEXPECTED)));
            continue;
        }
        let error_case = case == "callback-error";
        let late_case = case == "late";
        let work: Box<dyn FnOnce() + Send> = Box::new(move || {
            let outcome = callback(if error_case { Err(windows::core::Error(-77)) } else { Ok(()) }, Some(7));
            assert_eq!(outcome, if late_case { Err(windows::core::Error(E_UNEXPECTED)) } else { Ok(()) });
        });
        if case == "api" { let _api = scope.api(); work(); }
        else {
            { let _api = scope.api(); }
            match case.as_str() {
                "outside" => work(),
                "nested-scope" => { let _nested = webview2_com::rescue_host_delivery::Scope::begin(2); work(); },
                "other-thread" => { thread::spawn(work).join().unwrap(); },
                "get" | "duplicate-hook" | "missing-tail" | "callback-error" | "unwrapped-wait" | "wrong-token" => webview2_com::install(1, work),
                "dispatch" => webview2_com::install(2, work),
                "nested" => webview2_com::install(3, work),
                "pump-error" => webview2_com::install(4, work),
                "late" => webview2_com::install(5, work),
                _ => panic!("fixture case"),
            }
        }
        let value = if case == "unwrapped-wait" { webview2_com::wait_with_pump(rx) } else { scope.wait(rx) };
        match case.as_str() {
            "callback-error" => assert_eq!(value, Ok(Err(Error::Windows(windows::core::Error(-77))))),
            "pump-error" => assert_eq!(value, Err(Error::Windows(windows::core::Error(-19)))),
            "late" => { assert_eq!(value, Err(Error::TaskCanceled)); drop(scope); webview2_com::invoke(); continue; },
            _ => assert_eq!(value, Ok(Ok(7))),
        }
    }
    print!("{}", p.snapshot(true, true));
}

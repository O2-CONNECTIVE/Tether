//! Actual patched pinned pump/protocol/recorder, fake retrieval and dispatch only.
#![allow(non_snake_case, dead_code)]
use std::{
    cell::RefCell,
    sync::{
        Arc, Barrier,
        atomic::{AtomicUsize, Ordering::SeqCst},
        mpsc,
    },
    thread,
};
#[cfg(windows)]
#[link(name = "kernel32")]
unsafe extern "system" {
    fn GetLastError() -> u32;
    fn SetLastError(value: u32);
}
fn error_value() -> i32 {
    #[cfg(windows)]
    {
        unsafe { GetLastError() as i32 }
    }
    #[cfg(not(windows))]
    {
        0x51ab67
    }
}
fn pump_adapter_before_tls() {
    #[cfg(windows)]
    unsafe {
        SetLastError(0x1234);
    }
}
pub mod windows {
    pub mod core {
        #[derive(Debug, PartialEq)]
        pub struct Error(pub i32);
        impl Error {
            pub fn from_thread() -> Self {
                Self(crate::error_value())
            }
        }
    }
}
#[derive(Debug, PartialEq)]
pub enum Error {
    TaskCanceled,
    Windows(windows::core::Error),
}
impl From<windows::core::Error> for Error {
    fn from(v: windows::core::Error) -> Self {
        Self::Windows(v)
    }
}
pub type Result<T> = std::result::Result<T, Error>;
#[derive(Default)]
struct MSG;
struct Bool(i32);
struct State {
    case: String,
    entered: Barrier,
    release: Barrier,
    get: AtomicUsize,
    dispatch: AtomicUsize,
    translate: AtomicUsize,
}
thread_local! {
    static STATE: RefCell<Option<Arc<State>>> = const { RefCell::new(None) };
    static SENDER: RefCell<Option<mpsc::Sender<u8>>> = const { RefCell::new(None) };
}
fn state() -> Arc<State> {
    STATE.with(|s| s.borrow().as_ref().unwrap().clone())
}
fn hold(s: &State) {
    s.entered.wait();
    s.release.wait();
}
mod WindowsAndMessaging {
    use super::*;
    pub unsafe fn GetMessageA(_: &mut MSG, _: Option<()>, _: u32, _: u32) -> Bool {
        let s = state();
        let n = s.get.fetch_add(1, SeqCst) + 1;
        match s.case.as_str() {
            "get-held" => {
                hold(&s);
                Bool(0)
            }
            "caps" if n == 4 => {
                hold(&s);
                Bool(0)
            }
            "error" => {
                #[cfg(windows)]
                unsafe {
                    SetLastError(0x51ab67);
                }
                Bool(-1)
            }
            "quit" | "disconnected" => Bool(0),
            "other-negative" => Bool(-7),
            "positive" | "dispatch-held" | "caps" => Bool(1),
            _ => panic!("unexpected GetMessage"),
        }
    }
    pub unsafe fn TranslateMessage(_: &MSG) {
        state().translate.fetch_add(1, SeqCst);
    }
    pub unsafe fn DispatchMessageA(_: &MSG) {
        let s = state();
        s.dispatch.fetch_add(1, SeqCst);
        if s.case == "dispatch-held" {
            hold(&s);
        }
        if s.case != "caps" {
            SENDER.with(|tx| tx.borrow().as_ref().unwrap().send(7).unwrap());
        }
    }
}
#[path = "protocol.rs"]
pub mod rescue_host_delivery;
include!("pump.rs");
include!("recorder_path.rs");
fn main() {
    let case = std::env::args().nth(1).unwrap();
    #[cfg(windows)]
    if case != "error" {
        thread::spawn(|| {
            unsafe {
                SetLastError(0x51ab67);
            }
            assert_eq!(rescue_host_delivery::Pump::get_return(-1), -1);
            assert_eq!(
                unsafe { GetLastError() },
                0x51ab67,
                "first-use TLS path lost LastError"
            );
        })
        .join()
        .unwrap();
    }
    let poll_case = std::env::args().nth(2).unwrap_or_default();
    let foreign_ready = || thread::spawn(|| {
        let (tx, rx) = mpsc::channel(); tx.send(9_u8).unwrap();
        assert_eq!(wait_with_pump(rx), Ok(9)); // No TLS scope and no Get/Dispatch call.
    }).join().unwrap();
    let p = recorder::Probe::memory();
    assert!(p.register());
    let s = Arc::new(State {
        case: case.clone(),
        entered: Barrier::new(2),
        release: Barrier::new(2),
        get: AtomicUsize::new(0),
        dispatch: AtomicUsize::new(0),
        translate: AtomicUsize::new(0),
    });
    let owner = s.clone();
    let worker = thread::spawn(move || {
        STATE.with(|v| *v.borrow_mut() = Some(owner.clone()));
        let scope = rescue_host_delivery::Scope::begin(1);
        {
            let _api = scope.api();
        }
        let (tx, rx) = mpsc::channel();
        if owner.case == "ready" {
            tx.send(7).unwrap();
        }
        if owner.case == "disconnected" {
            drop(tx);
        } else {
            SENDER.with(|v| *v.borrow_mut() = Some(tx));
        }
        scope.wait(rx)
    });
    let held = matches!(case.as_str(), "get-held" | "dispatch-held" | "caps");
    let frozen = if held {
        s.entered.wait();
        if poll_case == "foreign-after-old-seal" { let _ = p.snapshot(true, true); }
        if poll_case.starts_with("foreign-") { foreign_ready(); }
        let text = p.pump_snapshot(true, true);
        s.release.wait();
        Some(text)
    } else {
        None
    };
    let result = worker.join().unwrap();
    if poll_case == "outside-ready" { foreign_ready(); }
    if poll_case == "outside-quit" {
        thread::spawn(|| {
            let outside = Arc::new(State { case: "quit".into(), entered: Barrier::new(1), release: Barrier::new(1), get: AtomicUsize::new(0), dispatch: AtomicUsize::new(0), translate: AtomicUsize::new(0) });
            STATE.with(|v| *v.borrow_mut() = Some(outside.clone()));
            let (_tx, rx) = mpsc::channel::<u8>();
            assert_eq!(wait_with_pump(rx), Err(Error::TaskCanceled));
            assert_eq!(outside.get.load(SeqCst), 1);
        }).join().unwrap();
    }
    let good = matches!(
        case.as_str(),
        "ready" | "positive" | "other-negative" | "dispatch-held"
    );
    assert_eq!(
        result,
        if good {
            Ok(7)
        } else if case == "error" {
            Err(Error::Windows(windows::core::Error(0x51ab67)))
        } else {
            Err(Error::TaskCanceled)
        }
    );
    let expected_get = if case == "ready" {
        0
    } else if case == "caps" {
        4
    } else {
        1
    };
    let expected_dispatch = if case == "caps" {
        3
    } else if matches!(
        case.as_str(),
        "positive" | "other-negative" | "dispatch-held"
    ) {
        1
    } else {
        0
    };
    assert_eq!(s.get.load(SeqCst), expected_get);
    assert_eq!(s.dispatch.load(SeqCst), expected_dispatch);
    assert_eq!(s.translate.load(SeqCst), expected_dispatch);
    let snapshot = p.pump_snapshot(true, true);
    if let Some(frozen) = frozen {
        assert_eq!(snapshot, frozen, "late return changed frozen snapshot");
    }
    for scalar in [-1, 0, 1, -7, i32::MIN, i32::MAX] {
        assert_eq!(
            rescue_host_delivery::Pump::get_return(scalar),
            scalar,
            "scalar adapter changed value"
        );
    }
    assert_eq!(snapshot, p.pump_snapshot(true, true));
    println!("{}", snapshot);
}

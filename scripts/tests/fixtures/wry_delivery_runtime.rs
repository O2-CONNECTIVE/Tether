//! SIMULATED SDK delivery through the actual patched pump/protocol and callback body.
use std::{
    cell::{Cell, RefCell},
    sync::mpsc,
};
pub mod windows {
    pub mod core {
        #[derive(Debug, PartialEq)]
        pub struct Error(pub i32);
        impl From<i32> for Error {
            fn from(v: i32) -> Self {
                Self(v)
            }
        }
        impl Error {
            pub fn from_thread() -> Self {
                Self(-19)
            }
        }
        pub type Result<T> = std::result::Result<T, Error>;
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
thread_local! {
    static MODE: Cell<u8> = const { Cell::new(0) };
    static PENDING: RefCell<Option<Box<dyn FnOnce()>>> = const { RefCell::new(None) };
}
pub fn install(mode: u8, callback: Box<dyn FnOnce()>) {
    MODE.with(|v| v.set(mode));
    PENDING.with(|v| *v.borrow_mut() = Some(callback));
}
pub fn invoke() {
    let callback = PENDING.with(|v| v.borrow_mut().take().unwrap());
    callback();
}
mod WindowsAndMessaging {
    use super::*;
    pub unsafe fn GetMessageA(_: &mut MSG, _: Option<()>, _: u32, _: u32) -> Bool {
        match MODE.with(Cell::get) {
            1 => {
                invoke();
                Bool(1)
            }
            2 => Bool(1),
            3 => {
                let (tx, rx) = mpsc::channel();
                tx.send(0_u8).unwrap();
                assert_eq!(wait_with_pump(rx).unwrap(), 0);
                invoke();
                Bool(1)
            }
            4 => Bool(-1),
            5 => Bool(0),
            _ => panic!("fixture mode"),
        }
    }
    pub unsafe fn TranslateMessage(_: &MSG) {}
    pub unsafe fn DispatchMessageA(_: &MSG) {
        if MODE.with(Cell::get) == 2 {
            invoke();
        }
    }
}
// run by the test from actual materialized source, no handwritten pump/protocol copy.
#[path = "protocol.rs"]
pub mod rescue_host_delivery;
include!("pump.rs");

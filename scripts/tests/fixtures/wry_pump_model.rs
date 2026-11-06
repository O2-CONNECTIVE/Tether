// SIMULATED DELIVERY ONLY. No Windows API, COM, WebView2, or app is invoked.
#![allow(non_snake_case, dead_code)]
use std::{
    cell::RefCell,
    sync::{
        atomic::{AtomicUsize, Ordering},
        mpsc, Arc, Condvar, Mutex,
    },
    thread,
    time::Duration,
};

#[derive(Debug, PartialEq)]
enum Error {
    TaskCanceled,
    Os,
}
type Result<T> = std::result::Result<T, Error>;
mod windows {
    pub mod core {
        pub struct Error;
        impl Error {
            pub fn from_thread() -> Self {
                Self
            }
        }
    }
}
impl From<windows::core::Error> for Error {
    fn from(_: windows::core::Error) -> Self {
        Self::Os
    }
}
#[derive(Default)]
struct MSG;
struct Bool(i32);
#[derive(Clone, Copy)]
enum Delivery {
    Ready,
    Posted,
    Nonqueued,
}
struct State {
    delivery: Delivery,
    producer: Mutex<Option<mpsc::Sender<u8>>>,
    callback_sent: mpsc::Sender<()>,
    release: (Mutex<bool>, Condvar),
    get_calls: AtomicUsize,
    dispatch_calls: AtomicUsize,
}
thread_local! { static ACTIVE: RefCell<Option<Arc<State>>> = const { RefCell::new(None) }; }
fn state() -> Arc<State> {
    ACTIVE.with(|s| s.borrow().as_ref().unwrap().clone())
}
fn callback(s: &State) {
    s.producer.lock().unwrap().take().unwrap().send(7).unwrap();
}
mod WindowsAndMessaging {
    use super::*;
    // An intentionally modeled nonqueued callback can run while GetMessage is
    // held. The owner explicitly releases it with a simulated queued message.
    pub unsafe fn GetMessageA(_: &mut MSG, _: Option<()>, _: u32, _: u32) -> Bool {
        let s = state();
        s.get_calls.fetch_add(1, Ordering::SeqCst);
        match s.delivery {
            Delivery::Ready => Bool(0),
            Delivery::Posted => Bool(1),
            Delivery::Nonqueued => {
                callback(&s);
                s.callback_sent.send(()).unwrap();
                let (lock, cv) = &s.release;
                let (released, _) = cv
                    .wait_timeout_while(lock.lock().unwrap(), Duration::from_secs(2), |v| !*v)
                    .unwrap();
                Bool(if *released { 1 } else { 0 })
            }
        }
    }
    pub unsafe fn TranslateMessage(_: &MSG) {}
    pub unsafe fn DispatchMessageA(_: &MSG) {
        let s = state();
        s.dispatch_calls.fetch_add(1, Ordering::SeqCst);
        if matches!(s.delivery, Delivery::Posted) {
            callback(&s);
        }
    }
}

// Created by run.py from the checksum-pinned archive without editing its body.
include!("pump.rs");

fn start(
    delivery: Delivery,
) -> (
    Arc<State>,
    mpsc::Receiver<()>,
    mpsc::Receiver<Result<u8>>,
    thread::JoinHandle<()>,
) {
    let (tx, rx) = mpsc::channel();
    if matches!(delivery, Delivery::Ready) {
        tx.send(7).unwrap();
    }
    let (sent_tx, sent_rx) = mpsc::channel();
    let s = Arc::new(State {
        delivery,
        producer: Mutex::new(Some(tx)),
        callback_sent: sent_tx,
        release: (Mutex::new(false), Condvar::new()),
        get_calls: AtomicUsize::new(0),
        dispatch_calls: AtomicUsize::new(0),
    });
    let (done_tx, done_rx) = mpsc::channel();
    let worker = s.clone();
    let join = thread::spawn(move || {
        ACTIVE.with(|v| *v.borrow_mut() = Some(worker));
        done_tx.send(wait_with_pump(rx)).unwrap();
    });
    (s, sent_rx, done_rx, join)
}
#[test]
fn ready_is_checked_before_getmessage() {
    let (s, _, done, join) = start(Delivery::Ready);
    assert_eq!(done.recv_timeout(Duration::from_secs(1)).unwrap(), Ok(7));
    join.join().unwrap();
    assert_eq!(s.get_calls.load(Ordering::SeqCst), 0);
}
#[test]
fn posted_dispatch_is_followed_by_receiver_check() {
    let (s, _, done, join) = start(Delivery::Posted);
    assert_eq!(done.recv_timeout(Duration::from_secs(1)).unwrap(), Ok(7));
    join.join().unwrap();
    assert_eq!(s.get_calls.load(Ordering::SeqCst), 1);
    assert_eq!(s.dispatch_calls.load(Ordering::SeqCst), 1);
}
#[test]
fn nonqueued_send_does_not_itself_return_getmessage() {
    let (s, sent, done, join) = start(Delivery::Nonqueued);
    sent.recv_timeout(Duration::from_secs(1)).unwrap();
    // This is a barrier-controlled model, not a timing inference or a Win32 test.
    assert_eq!(done.try_recv(), Err(mpsc::TryRecvError::Empty));
    assert_eq!(s.dispatch_calls.load(Ordering::SeqCst), 0);
    {
        let mut released = s.release.0.lock().unwrap();
        *released = true;
        s.release.1.notify_one();
    }
    assert_eq!(done.recv_timeout(Duration::from_secs(1)).unwrap(), Ok(7));
    join.join().unwrap();
    assert_eq!(s.get_calls.load(Ordering::SeqCst), 1);
    assert_eq!(s.dispatch_calls.load(Ordering::SeqCst), 1);
}

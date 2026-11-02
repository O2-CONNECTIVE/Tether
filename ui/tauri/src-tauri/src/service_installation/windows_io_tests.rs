//! Synthetic ownership tests only; no Windows function is invoked.
use super::*;
use std::cell::{Cell, RefCell};
use std::collections::VecDeque;
use std::panic::{catch_unwind, AssertUnwindSafe};

struct Fake {
    clock: Cell<Instant>,
    polls: RefCell<VecDeque<Result<usize, u32>>>,
    log: RefCell<Vec<&'static str>>,
    start_error: Option<u32>,
    cancel_error: Option<u32>,
    close_error: bool,
    panic_result: bool,
}
impl Fake {
    fn new(polls: impl IntoIterator<Item = Result<usize, u32>>) -> Self {
        RELEASED.with(|v| v.set(0));
        Self {
            clock: Cell::new(Instant::now()),
            polls: RefCell::new(polls.into_iter().collect()),
            log: RefCell::new(Vec::new()),
            start_error: None,
            cancel_error: None,
            close_error: false,
            panic_result: false,
        }
    }
    fn run(&self) -> Result<Vec<u8>, Fault> {
        transfer(
            self,
            1usize as HANDLE,
            8,
            &AtomicBool::new(false),
            self.now() + Duration::from_millis(1),
            Duration::from_millis(2),
        )
    }
    fn released(&self) -> usize {
        RELEASED.with(Cell::get)
    }
}
impl Io for Fake {
    fn event(&self) -> Result<HANDLE, Fault> {
        self.log.borrow_mut().push("event");
        Ok(2usize as HANDLE)
    }
    unsafe fn start(
        &self,
        _: HANDLE,
        bytes: *mut u8,
        length: u32,
        _: *mut OVERLAPPED,
    ) -> Result<(), u32> {
        self.log.borrow_mut().push("start");
        if let Some(error) = self.start_error {
            return Err(error);
        }
        for n in 0..length as usize {
            unsafe { bytes.add(n).write(b'a') };
        }
        Err(ERROR_IO_PENDING)
    }
    unsafe fn result(&self, _: HANDLE, _: *mut OVERLAPPED) -> Result<usize, u32> {
        self.log.borrow_mut().push("result");
        assert!(
            !self.panic_result,
            "injected worker panic with pending allocation"
        );
        self.polls
            .borrow_mut()
            .pop_front()
            .unwrap_or(Err(ERROR_IO_INCOMPLETE))
    }
    unsafe fn cancel(&self, _: HANDLE, _: *mut OVERLAPPED) -> Result<(), u32> {
        self.log.borrow_mut().push("cancel");
        self.cancel_error.map_or(Ok(()), Err)
    }
    fn wait(&self, _: HANDLE) -> u32 {
        self.log.borrow_mut().push("wait");
        self.clock.set(self.now() + Duration::from_millis(1));
        WAIT_TIMEOUT
    }
    fn close_event(&self, _: HANDLE) -> Result<(), Fault> {
        self.log.borrow_mut().push("close");
        if self.close_error {
            Err(Fault::Unknown)
        } else {
            Ok(())
        }
    }
    fn now(&self) -> Instant {
        self.clock.get()
    }
    fn fatal(&self) -> ! {
        self.log.borrow_mut().push("fatal");
        panic!("synthetic fatal")
    }
}
#[test]
fn exact_completion_releases_only_after_result() {
    let api = Fake::new([Ok(8)]);
    assert_eq!(api.run(), Ok(vec![b'a'; 8]));
    assert_eq!(*api.log.borrow(), ["event", "start", "result", "close"]);
    assert_eq!(api.released(), 1);
}
#[test]
fn late_completion_and_aborted_completion_are_denials_not_abandonment() {
    for terminal in [Ok(8), Err(ERROR_OPERATION_ABORTED)] {
        let api = Fake::new([Err(ERROR_IO_INCOMPLETE), terminal]);
        assert_eq!(api.run(), Err(Fault::Denied));
        assert_eq!(
            *api.log.borrow(),
            ["event", "start", "result", "wait", "cancel", "result", "close"]
        );
        assert_eq!(api.released(), 1);
    }
}
#[test]
fn cancel_not_found_still_requires_observed_completion() {
    let mut api = Fake::new([Err(ERROR_IO_INCOMPLETE), Err(ERROR_IO_INCOMPLETE), Ok(8)]);
    api.cancel_error = Some(ERROR_NOT_FOUND);
    assert_eq!(api.run(), Err(Fault::Denied));
    assert_eq!(
        api.log.borrow().iter().filter(|v| **v == "result").count(),
        3
    );
    assert_eq!(api.released(), 1);
}
#[test]
fn unknown_cancel_result_or_cleanup_expiry_retains_raw_storage() {
    for case in 0..3 {
        let mut api = match case {
            0 => Fake::new([Err(5)]),
            _ => Fake::new([Err(ERROR_IO_INCOMPLETE)]),
        };
        if case == 1 {
            api.cancel_error = Some(5);
        }
        assert!(catch_unwind(AssertUnwindSafe(|| api.run())).is_err());
        assert!(api.log.borrow().contains(&"fatal"));
        assert!(!api.log.borrow().contains(&"close"));
        assert_eq!(api.released(), 0);
    }
}
#[test]
fn panic_during_pending_result_does_not_free_kernel_storage() {
    let mut api = Fake::new([]);
    api.panic_result = true;
    assert!(catch_unwind(AssertUnwindSafe(|| api.run())).is_err());
    assert_eq!(api.released(), 0);
    assert!(!api.log.borrow().contains(&"close"));
    // Outer retained-worker guard is separately tested; it invokes native
    // non-unwinding fatal before dropping any retained token/file resources.
}
#[test]
fn close_uncertainty_preserves_storage_and_fails_stop() {
    let mut api = Fake::new([Ok(8)]);
    api.close_error = true;
    assert!(catch_unwind(AssertUnwindSafe(|| api.run())).is_err());
    assert_eq!(
        *api.log.borrow(),
        ["event", "start", "result", "close", "fatal"]
    );
    assert_eq!(api.released(), 0);
}
#[test]
fn failed_start_is_terminal_but_expired_read_starts_nothing() {
    let mut api = Fake::new([]);
    api.start_error = Some(5);
    assert_eq!(api.run(), Err(Fault::Denied));
    assert_eq!(*api.log.borrow(), ["event", "start", "close"]);
    assert_eq!(api.released(), 1);
    api.log.borrow_mut().clear();
    assert_eq!(
        transfer(
            &api,
            1usize as HANDLE,
            8,
            &AtomicBool::new(false),
            api.now(),
            Duration::from_millis(1)
        ),
        Err(Fault::Denied)
    );
    assert!(api.log.borrow().is_empty());
}

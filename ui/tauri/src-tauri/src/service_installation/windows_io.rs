//! Timeout/cancel is not completion. Raw ownership follows windows_client::operation.
use super::super::{Calls, Fault};
use super::Native;
use std::cell::UnsafeCell;
use std::ptr::{null, null_mut};
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};
use windows_sys::Win32::Foundation::{
    GetLastError, ERROR_HANDLE_EOF, ERROR_IO_INCOMPLETE, ERROR_IO_PENDING, ERROR_NOT_FOUND,
    ERROR_OPERATION_ABORTED, HANDLE, WAIT_OBJECT_0, WAIT_TIMEOUT,
};
use windows_sys::Win32::Storage::FileSystem::ReadFile;
use windows_sys::Win32::System::Threading::{CreateEventW, WaitForSingleObject};
use windows_sys::Win32::System::IO::{CancelIoEx, GetOverlappedResult, OVERLAPPED};

// Private seam tests the actual ownership loop without making Windows calls.
trait Io {
    fn event(&self) -> Result<HANDLE, Fault>;
    unsafe fn start(
        &self,
        file: HANDLE,
        bytes: *mut u8,
        length: u32,
        op: *mut OVERLAPPED,
    ) -> Result<(), u32>;
    unsafe fn result(&self, file: HANDLE, op: *mut OVERLAPPED) -> Result<usize, u32>;
    unsafe fn cancel(&self, file: HANDLE, op: *mut OVERLAPPED) -> Result<(), u32>;
    fn wait(&self, event: HANDLE) -> u32;
    fn close_event(&self, event: HANDLE) -> Result<(), Fault>;
    fn now(&self) -> Instant;
    fn fatal(&self) -> !;
}
fn result(ok: i32) -> Result<(), u32> {
    if ok == 0 {
        Err(unsafe { GetLastError() })
    } else {
        Ok(())
    }
}
impl Io for Native {
    fn event(&self) -> Result<HANDLE, Fault> {
        let event = unsafe { CreateEventW(null(), 1, 0, null()) };
        if event.is_null() {
            Err(Fault::Denied)
        } else {
            Ok(event)
        }
    }
    unsafe fn start(
        &self,
        file: HANDLE,
        bytes: *mut u8,
        length: u32,
        op: *mut OVERLAPPED,
    ) -> Result<(), u32> {
        result(unsafe { ReadFile(file, bytes, length, null_mut(), op) })
    }
    unsafe fn result(&self, file: HANDLE, op: *mut OVERLAPPED) -> Result<usize, u32> {
        let mut count = 0;
        result(unsafe { GetOverlappedResult(file, op, &mut count, 0) })?;
        Ok(count as usize)
    }
    unsafe fn cancel(&self, file: HANDLE, op: *mut OVERLAPPED) -> Result<(), u32> {
        result(unsafe { CancelIoEx(file, op) })
    }
    fn wait(&self, event: HANDLE) -> u32 {
        unsafe { WaitForSingleObject(event, 10) }
    }
    fn close_event(&self, event: HANDLE) -> Result<(), Fault> {
        Calls::close(self, event as usize)
    }
    fn now(&self) -> Instant {
        Instant::now()
    }
    fn fatal(&self) -> ! {
        Calls::fatal(self)
    }
}
struct Work {
    op: UnsafeCell<OVERLAPPED>,
    data: Vec<u8>,
}
#[cfg(test)]
thread_local! { static RELEASED: std::cell::Cell<usize> = const { std::cell::Cell::new(0) }; }
#[cfg(test)]
impl Drop for Work {
    fn drop(&mut self) {
        RELEASED.with(|v| v.set(v.get() + 1));
    }
}

pub(super) fn read(
    api: &Native,
    file: HANDLE,
    length: usize,
    stop: &AtomicBool,
    deadline: Instant,
    cleanup: Duration,
) -> Result<Vec<u8>, Fault> {
    transfer(api, file, length, stop, deadline, cleanup)
}
fn transfer(
    api: &impl Io,
    file: HANDLE,
    length: usize,
    stop: &AtomicBool,
    deadline: Instant,
    cleanup: Duration,
) -> Result<Vec<u8>, Fault> {
    if length == 0
        || length > 256 * 1024
        || cleanup.is_zero()
        || stop.load(Ordering::Acquire)
        || api.now() >= deadline
    {
        return Err(Fault::Denied);
    }
    // Allocate before creating event; neither address moves once started.
    let mut work = Box::new(Work {
        op: UnsafeCell::new(unsafe { std::mem::zeroed() }),
        data: vec![0; length],
    });
    let event = api.event()?;
    work.op.get_mut().hEvent = event;
    let op = work.op.get();
    let data = work.data.as_mut_ptr();
    // Panic cannot free state/buffer/event; enclosing worker guard invokes fatal
    // before releasing held file/token resources. No pending Rust references.
    let raw = Box::into_raw(work);
    match unsafe { api.start(file, data, length as u32, op) } {
        Ok(()) | Err(ERROR_IO_PENDING) => {}
        Err(_) => {
            if api.close_event(event).is_err() {
                api.fatal();
            }
            drop(unsafe { Box::from_raw(raw) });
            return Err(Fault::Denied);
        }
    }
    let mut cancelled = false;
    let mut cleanup_deadline = None;
    let (count, completed) = loop {
        if !cancelled && (stop.load(Ordering::Acquire) || api.now() >= deadline) {
            cancelled = true;
            cleanup_deadline = api.now().checked_add(cleanup);
            if cleanup_deadline.is_none() {
                api.fatal();
            }
            match unsafe { api.cancel(file, op) } {
                Ok(()) | Err(ERROR_NOT_FOUND) => {}
                Err(_) => api.fatal(),
            }
        }
        match unsafe { api.result(file, op) } {
            Ok(count) => break (count, true),
            Err(ERROR_OPERATION_ABORTED | ERROR_HANDLE_EOF) => break (0, false),
            Err(ERROR_IO_INCOMPLETE | ERROR_IO_PENDING) => {}
            // Unknown query failure is NOT proof kernel references ended.
            Err(_) => api.fatal(),
        }
        if cleanup_deadline.is_some_and(|limit| api.now() >= limit) {
            api.fatal();
        }
        match api.wait(event) {
            WAIT_OBJECT_0 | WAIT_TIMEOUT => {}
            _ => api.fatal(),
        }
    };
    // Close failure retains raw allocation: no retry or cleanup-success claim.
    if api.close_event(event).is_err() {
        api.fatal();
    }
    let mut work = unsafe { Box::from_raw(raw) };
    if !completed
        || cancelled
        || stop.load(Ordering::Acquire)
        || api.now() >= deadline
        || count > length
    {
        return Err(Fault::Denied);
    }
    work.data.truncate(count);
    Ok(std::mem::take(&mut work.data))
}
#[cfg(test)]
#[path = "windows_io_tests.rs"]
mod tests;

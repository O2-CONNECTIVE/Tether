//! Diagnostic-only private binding; new brackets save/restore Win32 LastError only.
use std::{cell::Cell, sync::mpsc};

unsafe extern "C" {
    fn rescue_host_delivery_pump(token: u64, bound: bool, event: u8);
    fn rescue_host_delivery_receiver(token: u64, bound: bool, state: u8);
    fn rescue_host_delivery_begin(kind: u8) -> u64;
    fn rescue_host_delivery_event(token: u64, code: u8, context: u8);
}

// Save before TLS or host lookup, including first use, not just inside the host hook.
#[cfg(windows)]
mod last_error {
    #[link(name = "kernel32")]
    unsafe extern "system" { fn GetLastError() -> u32; fn SetLastError(value: u32); }
    pub struct Saved(u32);
    impl Saved { pub fn save() -> Self { Self(unsafe { GetLastError() }) } }
    impl Drop for Saved { fn drop(&mut self) { unsafe { SetLastError(self.0); } } }
}
#[cfg(not(windows))]
mod last_error {
    pub struct Saved;
    impl Saved { pub fn save() -> Self { Self } }
}

#[derive(Clone, Copy, Default)]
struct Context {
    token: u64,
    armed: bool,
    pump_depth: u8,
    bound_wait: bool,
    phase: u8, // 0 outside, 1 SDK API, 2 GetMessage bracket, 3 Dispatch bracket
    uncertain: bool,
}
thread_local! { static CURRENT: Cell<Context> = const { Cell::new(Context {
    token: 0, armed: false, pump_depth: 0, bound_wait: false, phase: 0, uncertain: false,
}) }; }

fn emit(token: u64, code: u8) {
    CURRENT.with(|cell| {
        let c = cell.get();
        let matches = token != 0 && c.token == token;
        let bits = c.phase
            | (u8::from(c.bound_wait && c.pump_depth == 1) << 3)
            | (u8::from(matches) << 4)
            | (u8::from(c.uncertain || !matches) << 5);
        unsafe {
            rescue_host_delivery_event(token, code, bits);
        }
    });
}

struct Restore(Context);
impl Drop for Restore {
    fn drop(&mut self) {
        CURRENT.with(|cell| {
            let mut previous = self.0;
            previous.uncertain |= cell.get().uncertain;
            cell.set(previous);
        });
    }
}

pub struct Scope {
    token: u64,
    previous: Context,
}
impl Scope {
    pub fn begin(kind: u8) -> Self {
        let token = unsafe { rescue_host_delivery_begin(kind) };
        let previous = CURRENT.with(|cell| {
            let previous = cell.get();
            cell.set(Context {
                token,
                uncertain: previous.uncertain
                    || previous.token != 0
                    || previous.pump_depth != 0
                    || token == 0,
                ..Context::default()
            });
            previous
        });
        if previous.token != 0 {
            emit(previous.token, 10);
        }
        Self { token, previous }
    }
    pub fn token(&self) -> u64 {
        self.token
    }
    pub fn api(&self) -> Api {
        let previous = CURRENT.with(|cell| {
            let mut c = cell.get();
            let previous = c;
            c.uncertain |= c.token != self.token || c.phase != 0 || c.pump_depth != 0;
            c.phase = 1;
            cell.set(c);
            previous
        });
        emit(self.token, 1);
        Api {
            token: self.token,
            restore: Restore(previous),
        }
    }
    pub fn wait<T>(&self, rx: mpsc::Receiver<T>) -> crate::Result<T> {
        let previous = CURRENT.with(|cell| {
            let mut c = cell.get();
            let previous = c;
            c.uncertain |= c.token != self.token || c.armed || c.pump_depth != 0 || c.phase != 0;
            c.armed = true;
            cell.set(c);
            previous
        });
        let _restore = Restore(previous);
        // The exact original function consumes the exact original Receiver once.
        crate::wait_with_pump(rx)
    }
}
impl Drop for Scope {
    fn drop(&mut self) {
        emit(self.token, 9);
        let _restore = Restore(self.previous);
    }
}
pub struct Api {
    token: u64,
    restore: Restore,
}
impl Drop for Api {
    fn drop(&mut self) {
        emit(self.token, 2);
        // Restore's field drop follows this receipt, including error/unwind paths.
        let _ = &self.restore;
    }
}

pub struct Completion {
    token: u64,
}
impl Completion {
    pub fn enter(token: u64) -> Self {
        emit(token, 5);
        Self { token }
    }
    pub fn sent(token: u64, success: bool) {
        emit(token, if success { 6 } else { 7 });
    }
}
impl Drop for Completion {
    fn drop(&mut self) {
        emit(self.token, 8);
    }
}

pub(crate) struct Pump {
    token: u64,
    original: bool,
    restore: Restore,
}
impl Pump {
    pub(crate) fn enter() -> Self {
        let (previous, original) = CURRENT.with(|cell| {
            let mut c = cell.get();
            let previous = c;
            let original = c.token != 0 && c.armed && c.pump_depth == 0;
            c.pump_depth = c.pump_depth.saturating_add(1);
            c.armed = false;
            c.bound_wait = original;
            c.uncertain |= c.token != 0 && (!original || c.pump_depth != 1);
            c.phase = 0;
            cell.set(c);
            (previous, original)
        });
        if previous.token != 0 {
            emit(previous.token, if original { 3 } else { 10 });
        }
        Self {
            token: previous.token,
            original,
            restore: Restore(previous),
        }
    }
    // Identity adapter: one original receive, no payload inspection or ownership change.
    pub(crate) fn observe<T>(
        value: Result<T, mpsc::TryRecvError>,
    ) -> Result<T, mpsc::TryRecvError> {
        let state = match &value {
            Ok(_) => 0,
            Err(mpsc::TryRecvError::Empty) => 1,
            Err(mpsc::TryRecvError::Disconnected) => 2,
        };
        CURRENT.with(|cell| {
            let c = cell.get();
            let bound =
                c.token != 0 && c.bound_wait && c.pump_depth == 1 && !c.uncertain && c.phase == 0;
            unsafe {
                rescue_host_delivery_receiver(c.token, bound, state);
            }
        });
        value
    }
    pub(crate) fn phase(value: u8) {
        let _last_error = last_error::Saved::save();
        if value == 2 || value == 3 { Self::bracket(if value == 2 { 0 } else { 4 }); }
        CURRENT.with(|cell| {
            let mut c = cell.get();
            c.phase = value;
            cell.set(c);
        });
    }
    fn bracket(event: u8) {
        CURRENT.with(|cell| {
            let c = cell.get();
            let bound = c.token != 0 && c.bound_wait && c.pump_depth == 1 && !c.uncertain;
            unsafe { rescue_host_delivery_pump(c.token, bound, event); }
        });
    }
    // Scalar identity only. The original match and Error::from_thread stay untouched.
    pub(crate) fn get_return(value: i32) -> i32 {
        let _last_error = last_error::Saved::save();
        #[cfg(rescue_host_pump_fixture)]
        crate::pump_adapter_before_tls();
        Self::bracket(match value { -1 => 1, 0 => 2, _ => 3 });
        value
    }
    pub(crate) fn dispatch_return() {
        let _last_error = last_error::Saved::save();
        Self::bracket(5);
        Self::phase(0);
    }

}
impl Drop for Pump {
    fn drop(&mut self) {
        if self.token != 0 && self.original {
            emit(self.token, 4);
        }
        let _ = &self.restore;
    }
}

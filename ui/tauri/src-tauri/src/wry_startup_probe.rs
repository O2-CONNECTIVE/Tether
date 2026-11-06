//! Diagnostic overlay only. No raw identity, HRESULT, script, path or payload is recorded.
use std::sync::{
    atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering::SeqCst},
    Arc, Mutex, OnceLock, Weak,
};
use std::thread::{self, ThreadId};
use std::time::Instant;

const NAMES: [&str; 8] = [
    "environment-enter",
    "environment-api-ok",
    "environment-result-sent",
    "environment-wait-returned",
    "controller-enter",
    "controller-api-ok",
    "controller-result-sent",
    "controller-wait-returned",
];
static ACTIVE: OnceLock<Weak<Probe>> = OnceLock::new();

struct Event {
    code: u8,
    seq: u64,
    elapsed: u128,
    on_trace_thread: bool,
}
pub(super) struct Probe {
    start: Instant,
    trace_thread: ThreadId,
    events: Mutex<Vec<Event>>,
    attempted: AtomicU64,
    dropped: AtomicU64,
    in_flight: AtomicUsize,
    enabled: AtomicBool,
}
impl Probe {
    pub(super) fn memory() -> Arc<Self> {
        Arc::new(Self {
            start: Instant::now(),
            trace_thread: thread::current().id(),
            events: Mutex::new(Vec::with_capacity(8)),
            attempted: AtomicU64::new(0),
            dropped: AtomicU64::new(0),
            in_flight: AtomicUsize::new(0),
            enabled: AtomicBool::new(true),
        })
    }
    pub(super) fn register(self: &Arc<Self>) -> bool {
        ACTIVE.set(Arc::downgrade(self)).is_ok()
    }
    fn record(&self, code: u8) {
        self.in_flight.fetch_add(1, SeqCst);
        if self.enabled.load(SeqCst) {
            let seq = self.attempted.fetch_add(1, SeqCst) + 1;
            let mut stored = false;
            if (1..=8).contains(&code) {
                if let Ok(mut events) = self.events.try_lock() {
                    if events.len() < 8 {
                        events.push(Event {
                            code,
                            seq,
                            elapsed: self.start.elapsed().as_micros(),
                            on_trace_thread: thread::current().id() == self.trace_thread,
                        });
                        stored = true;
                    }
                }
            }
            if !stored {
                self.dropped.fetch_add(1, SeqCst);
            }
        }
        self.in_flight.fetch_sub(1, SeqCst);
    }
    // Worker only. Do not wait for a callback or acquire a blocking UI lock.
    pub(super) fn snapshot(&self, registration: bool, requested: bool) -> String {
        self.enabled.store(false, SeqCst);
        let seal_elapsed = self.start.elapsed().as_micros();
        let in_flight = self.in_flight.load(SeqCst);
        let rows =
            self.events
                .try_lock()
                .ok()
                .map(|v| {
                    let mut sorted: Vec<_> = v.iter().collect();
                    sorted.sort_by_key(|e| e.seq);
                    sorted
                        .iter()
                        .map(|e| {
                            format!(
                "{{\"seq\":{},\"stage\":\"{}\",\"elapsedUs\":{},\"onTraceThread\":{}}}",
                e.seq, NAMES[usize::from(e.code - 1)], e.elapsed, e.on_trace_thread
            )
                        })
                        .collect::<Vec<_>>()
                })
                .unwrap_or_default();
        format!("{{\"schema\":1,\"variant\":\"wry-completion-probe-1\",\"purpose\":\"diagnostic-hints-only\",\"registered\":{},\"sealed\":{},\"sealElapsedUs\":{},\"attempted\":{},\"recorded\":{},\"dropped\":{},\"inFlight\":{},\"events\":[{}]}}",
            registration, requested && in_flight == 0, seal_elapsed, self.attempted.load(SeqCst),
            rows.len(), self.dropped.load(SeqCst), in_flight, rows.join(","))
    }
}

// The patched Wry dependency links this symbol only in the explicit diagnostic build.
// Fixed event storage; no explicit event allocation, I/O, blocking lock or suspension.
// thread::current may lazily initialize a foreign callback thread's thread handle.
#[unsafe(no_mangle)]
pub extern "C" fn rescue_host_wry_stage(code: u8) {
    if let Some(probe) = ACTIVE.get().and_then(Weak::upgrade) {
        probe.record(code);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn all_codes_are_bounded_and_seal_suppresses_later_receipts() {
        let p = Probe::memory();
        for code in 1..=8 {
            p.record(code);
        }
        let text = p.snapshot(true, true);
        assert!(text.len() < 2048);
        assert!(text.contains("\"attempted\":8,\"recorded\":8,\"dropped\":0"));
        assert_eq!(text.matches("\"onTraceThread\":true").count(), 8);
        p.record(1);
        assert_eq!(p.attempted.load(SeqCst), 8);
    }
    #[test]
    fn contention_capacity_and_invalid_codes_remain_visible() {
        let p = Probe::memory();
        let held = p.events.lock().unwrap();
        p.record(1);
        drop(held);
        p.record(0);
        p.record(255);
        for _ in 0..9 {
            p.record(1);
        }
        let text = p.snapshot(true, true);
        assert!(text.contains("\"attempted\":12,\"recorded\":8,\"dropped\":4"));
    }
    #[test]
    fn cross_thread_receipt_is_a_boolean_only() {
        let p = Probe::memory();
        let other = p.clone();
        thread::spawn(move || other.record(3)).join().unwrap();
        assert!(p.snapshot(true, true).contains("\"onTraceThread\":false"));
    }
    #[test]
    fn missing_finish_registration_or_flight_never_claim_complete() {
        let p = Probe::memory();
        assert!(p
            .snapshot(false, false)
            .contains("\"registered\":false,\"sealed\":false"));
        let p = Probe::memory();
        p.in_flight.store(1, SeqCst);
        assert!(p.snapshot(true, true).contains("\"sealed\":false"));
    }
}

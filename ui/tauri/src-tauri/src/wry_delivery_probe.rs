//! Fixed diagnostic storage. No path, payload, raw token, thread ID, or HRESULT leaves it.
#[path = "wry_receiver_probe.rs"]
mod receiver;
#[path = "wry_pump_probe.rs"]
mod pump;
use std::sync::{
    Arc, OnceLock,
    atomic::{AtomicBool, AtomicU8, AtomicU64, AtomicUsize, Ordering::SeqCst},
};
use std::time::Instant;

// One bounded process-lifetime allocation. Hooks borrow it, so neither upgrading
// a weak reference nor dropping the last Arc can invoke an allocator here.
static ACTIVE: OnceLock<Arc<Probe>> = OnceLock::new();
const SLOTS: [&str; 2] = ["environment", "controller"];
const EVENTS: [&str; 3] = [
    "completion-closure-enter",
    "send-result",
    "closure-guard-drop",
];

#[cfg(windows)]
mod platform {
    #[link(name = "kernel32")]
    unsafe extern "system" {
        fn GetCurrentThreadId() -> u32;
        fn GetLastError() -> u32;
        fn SetLastError(value: u32);
    }
    #[link(name = "ole32")]
    unsafe extern "system" {
        fn CoGetApartmentType(kind: *mut i32, qualifier: *mut i32) -> i32;
    }
    #[link(name = "user32")]
    unsafe extern "system" {
        fn InSendMessageEx(reserved: *mut std::ffi::c_void) -> u32;
    }
    pub fn sent_context() -> u8 {
        let flags = unsafe { InSendMessageEx(std::ptr::null_mut()) };
        if flags == 0 {
            2
        } else if flags & !15 == 0 {
            1
        } else {
            0
        }
    }
    pub fn thread() -> u64 {
        u64::from(unsafe { GetCurrentThreadId() })
    }
    pub fn apartment() -> u8 {
        let (mut kind, mut qualifier) = (-1, -1);
        if unsafe { CoGetApartmentType(&mut kind, &mut qualifier) } != 0 {
            return 0;
        }
        match kind {
            0 => 1,
            3 => 2,
            1 | 2 => 3,
            _ => 0,
        }
    }
    pub struct ErrorState(u32);
    impl ErrorState {
        pub fn save() -> Self {
            Self(unsafe { GetLastError() })
        }
    }
    impl Drop for ErrorState {
        fn drop(&mut self) {
            unsafe {
                SetLastError(self.0);
            }
        }
    }
    #[cfg(test)]
    #[test]
    fn diagnostic_queries_preserve_last_error() {
        let original = unsafe { GetLastError() };
        unsafe {
            SetLastError(0x51ab67);
        }
        {
            let _restore = ErrorState::save();
            let _ = thread();
            let _ = apartment();
            let _ = sent_context();
            unsafe {
                SetLastError(0x1234);
            }
        }
        assert_eq!(unsafe { GetLastError() }, 0x51ab67);
        unsafe {
            SetLastError(original);
        }
    }
}
#[cfg(not(windows))]
mod platform {
    use std::{
        cell::Cell,
        sync::atomic::{AtomicU64, Ordering::SeqCst},
    };
    static NEXT: AtomicU64 = AtomicU64::new(1);
    thread_local! { static KEY: Cell<u64> = const { Cell::new(0) }; }
    pub fn thread() -> u64 {
        KEY.with(|key| {
            if key.get() == 0 {
                key.set(NEXT.fetch_add(1, SeqCst));
            }
            key.get()
        })
    }
    pub fn apartment() -> u8 {
        0
    } // Local fixtures never invent Windows apartment evidence.
    pub fn sent_context() -> u8 {
        0
    }
    pub struct ErrorState;
    impl ErrorState {
        pub fn save() -> Self {
            Self
        }
    }
}

#[derive(Default)]
struct Scope {
    ready: AtomicU8,
    owner: AtomicU64,
    apartment: AtomicU8,
    api_enter: AtomicU8,
    api_drop: AtomicU8,
    wait_enter: AtomicU8,
    wait_drop: AtomicU8,
    scope_drop: AtomicU8,
    ambiguous: AtomicBool,
}
#[derive(Default)]
struct Sample {
    count: AtomicU8,
    ready: AtomicBool,
    seq: AtomicU8,
    elapsed: AtomicU64,
    phase: AtomicU8,
    relation: AtomicU8,
    on_scope_thread: AtomicBool,
    success: AtomicU8,
    sent_context: AtomicU8,
}
pub(super) struct Probe {
    receiver: receiver::Summary,
    pump: pump::Summary,
    start: Instant,
    trace_thread: u64,
    scopes: [Scope; 2],
    samples: [Sample; 6],
    attempts: AtomicU8,
    sequence: AtomicU8,
    loss: AtomicBool,
    in_flight: AtomicUsize,
    enabled: AtomicBool,
    seal_elapsed: AtomicU64,
}
struct Flight<'a>(&'a Probe);
impl Drop for Flight<'_> {
    fn drop(&mut self) {
        self.0.in_flight.fetch_sub(1, SeqCst);
    }
}
fn once(counter: &AtomicU8) -> bool {
    if counter.compare_exchange(0, 1, SeqCst, SeqCst).is_ok() {
        true
    } else {
        counter.store(2, SeqCst);
        false
    } // 2 means two-or-more, never wraps.
}
impl Probe {
    pub(super) fn memory() -> Arc<Self> {
        Arc::new(Self {
            receiver: receiver::Summary::default(),
            pump: pump::Summary::default(),
            start: Instant::now(),
            trace_thread: platform::thread(),
            scopes: std::array::from_fn(|_| Scope::default()),
            samples: std::array::from_fn(|_| Sample::default()),
            attempts: AtomicU8::new(0),
            sequence: AtomicU8::new(0),
            loss: AtomicBool::new(false),
            in_flight: AtomicUsize::new(0),
            enabled: AtomicBool::new(true),
            seal_elapsed: AtomicU64::new(0),
        })
    }
    pub(super) fn register(self: &Arc<Self>) -> bool {
        ACTIVE.set(self.clone()).is_ok()
    }
    fn flight(&self) -> Option<Flight<'_>> {
        let n = self.in_flight.load(SeqCst);
        if n >= 32
            || self
                .in_flight
                .compare_exchange(n, n + 1, SeqCst, SeqCst)
                .is_err()
        {
            self.loss.store(true, SeqCst);
            return None;
        }
        let flight = Flight(self);
        if !self.enabled.load(SeqCst) {
            return None;
        }
        Some(flight)
    }
    fn begin(&self, kind: u8) -> u64 {
        let writer = self.pump.writer(); // Admission before old validation and early-out.
        let token = self.begin_old(kind);
        if let Some(writer) = writer { writer.begin(kind, token); }
        token
    }
    fn begin_old(&self, kind: u8) -> u64 {
        let Some(_flight) = self.flight() else {
            return 0;
        };
        let n = self.attempts.load(SeqCst);
        if n >= 3
            || self
                .attempts
                .compare_exchange(n, n + 1, SeqCst, SeqCst)
                .is_err()
        {
            self.loss.store(true, SeqCst);
            return 0;
        }
        if !(1..=2).contains(&kind) {
            self.loss.store(true, SeqCst);
            return 0;
        }
        let slot = &self.scopes[usize::from(kind - 1)];
        if slot.ready.compare_exchange(0, 1, SeqCst, SeqCst).is_err() {
            slot.ambiguous.store(true, SeqCst);
            self.loss.store(true, SeqCst);
            return 0;
        }
        slot.owner.store(platform::thread(), SeqCst);
        slot.apartment.store(platform::apartment(), SeqCst); // Entry only, outside API/callback/pump.
        slot.ready.store(2, SeqCst);
        u64::from(kind) // Issued once per kind; private and never serialized or reused.
    }
    fn event(&self, token: u64, code: u8, context: u8) {
        if let Some(writer) = self.pump.writer() {
            writer.event(token, code, context, |i| self.scopes[i].owner.load(SeqCst) == platform::thread());
        }
        self.event_old(token, code, context);
    }
    fn event_old(&self, token: u64, code: u8, context: u8) {
        let Some(_flight) = self.flight() else {
            return;
        };
        if !(1..=2).contains(&token) || context & !0x3f != 0 || context & 7 > 3 {
            self.loss.store(true, SeqCst);
            return;
        }
        let index = token as usize - 1;
        let slot = &self.scopes[index];
        if slot.ready.load(SeqCst) != 2 {
            self.loss.store(true, SeqCst);
            return;
        }
        let on_thread = slot.owner.load(SeqCst) == platform::thread();
        let matches = context & 16 != 0;
        let uncertain =
            context & 32 != 0 || !matches || !on_thread || slot.scope_drop.load(SeqCst) != 0;
        if uncertain {
            slot.ambiguous.store(true, SeqCst);
        }
        let counter = match code {
            1 => Some(&slot.api_enter),
            2 => Some(&slot.api_drop),
            3 => Some(&slot.wait_enter),
            4 => Some(&slot.wait_drop),
            9 => Some(&slot.scope_drop),
            10 => {
                slot.ambiguous.store(true, SeqCst);
                None
            }
            _ => None,
        };
        if let Some(counter) = counter {
            if !once(counter) {
                slot.ambiguous.store(true, SeqCst);
            }
            return;
        }
        if code == 10 {
            return;
        }
        let (sample_index, success) = match code {
            5 => (0, 0),
            6 => (1, 2),
            7 => (1, 1),
            8 => (2, 0),
            _ => {
                self.loss.store(true, SeqCst);
                return;
            }
        };
        let sample = &self.samples[index * 3 + sample_index];
        if !once(&sample.count) {
            self.loss.store(true, SeqCst);
            return;
        }
        let seq = self.sequence.load(SeqCst);
        if seq >= 6
            || self
                .sequence
                .compare_exchange(seq, seq + 1, SeqCst, SeqCst)
                .is_err()
        {
            self.loss.store(true, SeqCst);
            return;
        }
        let elapsed = self.start.elapsed().as_micros();
        if elapsed > (1_u128 << 53) - 1 {
            self.loss.store(true, SeqCst);
            return;
        }
        let bound = !uncertain && !slot.ambiguous.load(SeqCst);
        sample.seq.store(seq + 1, SeqCst);
        sample.elapsed.store(elapsed as u64, SeqCst);
        sample
            .phase
            .store(if bound { context & 7 } else { 4 }, SeqCst);
        sample.relation.store(
            if !bound {
                0
            } else if context & 8 != 0 {
                2
            } else {
                1
            },
            SeqCst,
        );
        sample.on_scope_thread.store(on_thread, SeqCst);
        sample.success.store(success, SeqCst);
        sample
            .sent_context
            .store(if bound { platform::sent_context() } else { 0 }, SeqCst);
        sample.ready.store(true, SeqCst);
    }
    pub(super) fn pump_snapshot(&self, registered: bool, requested: bool) -> String {
        self.pump.snapshot(registered, requested)
    }
    pub(super) fn receiver_snapshot(&self, registered: bool, requested: bool) -> String {
        self.receiver.snapshot(registered, requested, || {
            // Existing delivery snapshot must seal first. A live/in-flight old
            // writer makes association unknown, not an invented atomic join.
            let stable = !self.enabled.load(SeqCst)
                && self.in_flight.load(SeqCst) == 0
                && !self.loss.load(SeqCst);
            std::array::from_fn(|i| !stable || self.scopes[i].ambiguous.load(SeqCst))
        })
    }
    pub(super) fn snapshot(&self, registered: bool, requested: bool) -> String {
        if self.enabled.swap(false, SeqCst) {
            let elapsed = self.start.elapsed().as_micros();
            if elapsed > (1_u128 << 53) - 1 {
                self.loss.store(true, SeqCst);
            }
            self.seal_elapsed
                .store(elapsed.min((1_u128 << 53) - 1) as u64, SeqCst);
        }
        let flight = self.in_flight.load(SeqCst);
        let scopes = self.scopes.iter().enumerate().map(|(i, s)| {
            let ready = s.ready.load(SeqCst);
            let state = match ready { 0 => "missing", 2 => "observed", _ => "interrupted" };
            let on_thread = if ready == 2 { (s.owner.load(SeqCst) == self.trace_thread).to_string() } else { "null".into() };
            let apartment = match if ready == 2 { s.apartment.load(SeqCst) } else { 0 } { 1 => "sta", 2 => "main-sta", 3 => "other", _ => "unknown" };
            format!("{{\"slot\":\"{}\",\"state\":\"{}\",\"scopeThreadIsTraceThread\":{},\"apartmentAtScopeEntry\":\"{}\",\"apiEntries\":{},\"apiGuardDrops\":{},\"waitEntries\":{},\"waitGuardDrops\":{},\"scopeGuardDrops\":{},\"ambiguous\":{}}}",
                SLOTS[i], state, on_thread, apartment, s.api_enter.load(SeqCst), s.api_drop.load(SeqCst),
                s.wait_enter.load(SeqCst), s.wait_drop.load(SeqCst), s.scope_drop.load(SeqCst), s.ambiguous.load(SeqCst))
        }).collect::<Vec<_>>();
        let samples = self.samples.iter().enumerate().map(|(i, s)| {
            let count = s.count.load(SeqCst);
            let observed = count == 1 && s.ready.load(SeqCst);
            let bound = observed && !self.scopes[i / 3].ambiguous.load(SeqCst);
            let state = if count == 0 { "missing" } else if count > 1 { "ambiguous" } else if observed { "observed" } else { "interrupted" };
            let phase = if bound { ["outside", "sdk-api", "get-message", "dispatch-message", "unknown"][usize::from(s.phase.load(SeqCst))] } else { "unknown" };
            let relation = if bound { ["unknown", "same-creation", "original-wait"][usize::from(s.relation.load(SeqCst))] } else { "unknown" };
            let sent_context = if bound { ["unknown", "sent-context", "not-sent-context"][usize::from(s.sent_context.load(SeqCst))] } else { "unknown" };
            let (seq, elapsed, on_thread, success) = if observed {
                (s.seq.load(SeqCst).to_string(), s.elapsed.load(SeqCst).to_string(), s.on_scope_thread.load(SeqCst).to_string(),
                 match s.success.load(SeqCst) { 1 => "false", 2 => "true", _ => "null" })
            } else { ("null".into(), "null".into(), "null".into(), "null") };
            format!("{{\"slot\":\"{}\",\"event\":\"{}\",\"state\":\"{}\",\"seq\":{},\"elapsedUs\":{},\"phase\":\"{}\",\"relation\":\"{}\",\"sentContext\":\"{}\",\"onScopeThread\":{},\"sendSucceeded\":{}}}",
                SLOTS[i / 3], EVENTS[i % 3], state, seq, elapsed, phase, relation, sent_context, on_thread, success)
        }).collect::<Vec<_>>();
        format!(
            "{{\"schema\":1,\"variant\":\"wry-sdk-delivery-1\",\"purpose\":\"diagnostic-hints-only\",\"registered\":{},\"sealed\":{},\"sealElapsedUs\":{},\"inFlight\":{},\"lossDetected\":{},\"scopeAttemptsCapped\":{},\"scopes\":[{}],\"samples\":[{}]}}",
            registered,
            requested && flight == 0,
            self.seal_elapsed.load(SeqCst),
            flight,
            self.loss.load(SeqCst),
            self.attempts.load(SeqCst),
            scopes.join(","),
            samples.join(",")
        )
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn rescue_host_delivery_begin(kind: u8) -> u64 {
    let _last_error = platform::ErrorState::save();
    ACTIVE.get().map_or(0, |p| p.begin(kind))
}
#[unsafe(no_mangle)]
pub extern "C" fn rescue_host_delivery_event(token: u64, code: u8, context: u8) {
    let _last_error = platform::ErrorState::save();
    if let Some(p) = ACTIVE.get() {
        p.event(token, code, context);
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn rescue_host_delivery_pump(token: u64, bound: bool, event: u8) {
    let _last_error = platform::ErrorState::save();
    if let Some(p) = ACTIVE.get() {
        if let Some(writer) = p.pump.writer() {
            writer.bracket(token, bound, event, |i| p.scopes[i].owner.load(SeqCst) == platform::thread());
        }
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn rescue_host_delivery_receiver(token: u64, bound: bool, state: u8) {
    let _last_error = platform::ErrorState::save();
    if let Some(p) = ACTIVE.get() {
        if let Some(writer) = p.pump.writer() {
            writer.receiver(token, bound, |i| p.scopes[i].owner.load(SeqCst) == platform::thread());
        }
        let bucket = if bound && (1..=2).contains(&token) {
            let slot = &p.scopes[token as usize - 1];
            if slot.ready.load(SeqCst) == 2
                && !slot.ambiguous.load(SeqCst)
                && slot.owner.load(SeqCst) == platform::thread()
                && slot.wait_enter.load(SeqCst) == 1
                && slot.wait_drop.load(SeqCst) == 0
                && slot.scope_drop.load(SeqCst) == 0
            {
                token as usize - 1
            } else {
                2
            }
        } else {
            2
        };
        p.receiver.observe(bucket, state);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pump_association_stays_live_after_old_delivery_seal() {
        for late_begin in [false, true] {
            let p = Probe::memory(); let token = p.begin(1); p.event(token, 3, 24);
            let _old = p.snapshot(true, true);
            if late_begin { assert_eq!(p.begin(2), 0); }
            else { p.event(token, 10, 48); }
            let first = p.pump_snapshot(true, true);
            assert!(first.contains("\"lossDetected\":true"));
            assert!(!first.contains("\"relation\":\"original-wait\""));
            p.event(token, 4, 24);
            assert_eq!(first, p.pump_snapshot(true, true));
        }
    }

    #[test]
    fn receiver_association_requires_closed_old_window() {
        let p = Probe::memory(); p.begin(1); p.receiver.observe(0, 1);
        assert!(!p.receiver_snapshot(true, true).contains("\"relation\":\"original-wait\""));
        let p = Probe::memory(); let token = p.begin(1); p.receiver.observe(0, 1);
        let _ = p.snapshot(true, true);
        let before = p.receiver_snapshot(true, true);
        assert!(before.contains("\"relation\":\"original-wait\""));
        p.event(token, 10, 32); // Post-seal old event cannot mutate association.
        assert_eq!(before, p.receiver_snapshot(true, true));
        let p = Probe::memory(); p.begin(1); p.receiver.observe(0, 1);
        p.in_flight.store(1, SeqCst); let _ = p.snapshot(true, true);
        assert!(!p.receiver_snapshot(true, true).contains("\"relation\":\"original-wait\""));
    }

    #[test]
    fn receiver_wrong_token_and_other_thread_are_unbound() {
        // Isolated process fixture registration; no mutation of any channel ownership.
        let p = Probe::memory();
        assert!(p.register());
        let token = p.begin(1);
        p.event(token, 3, 24);
        rescue_host_delivery_receiver(99, true, 1);
        std::thread::spawn(move || rescue_host_delivery_receiver(token, true, 2))
            .join()
            .unwrap();
        let text = p.receiver_snapshot(true, true);
        assert!(text.contains("\"bucket\":\"unbound\",\"relation\":\"unknown\",\"readyCapped\":0,\"emptyCapped\":1,\"disconnectedCapped\":1"));
    }

    #[test]
    fn fixed_slots_and_samples_preserve_context_without_identifiers() {
        let p = Probe::memory();
        for kind in 1..=2 {
            let token = p.begin(kind);
            for (code, context) in [
                (1, 17),
                (2, 16),
                (3, 24),
                (5, 26),
                (6, 26),
                (8, 26),
                (4, 24),
                (9, 16),
            ] {
                p.event(token, code, context);
            }
        }
        let text = p.snapshot(true, true);
        assert!(text.len() < 8192);
        assert_eq!(text.matches("\"phase\":\"get-message\"").count(), 6);
        assert_eq!(text.matches("\"relation\":\"original-wait\"").count(), 6);
        assert!(!text.contains("owner") && !text.contains("token") && !text.contains("threadId"));
        p.event(1, 5, 26);
        assert_eq!(p.snapshot(true, true), text);
    }

    #[test]
    fn duplicate_unknown_token_context_and_scope_capacity_never_pass() {
        let p = Probe::memory();
        let token = p.begin(1);
        p.event(token, 5, 17);
        p.event(token, 5, 17);
        p.event(99, 6, 26);
        p.event(token, 8, 255);
        assert_eq!(p.begin(1), 0);
        assert!(p.snapshot(true, true).contains("\"lossDetected\":true"));
        let p = Probe::memory();
        let token = p.begin(1);
        p.event(token, 5, 26 | 32);
        let text = p.snapshot(true, true);
        assert!(text.contains("\"phase\":\"unknown\",\"relation\":\"unknown\""));
        assert!(text.contains("\"ambiguous\":true"));
    }

    #[test]
    fn other_thread_missing_and_interrupted_samples_stay_unknown() {
        let p = Probe::memory();
        let token = p.begin(1);
        let other = p.clone();
        std::thread::spawn(move || other.event(token, 5, 26))
            .join()
            .unwrap();
        let text = p.snapshot(true, true);
        assert!(text.contains("\"onScopeThread\":false"));
        assert!(text.contains("\"phase\":\"unknown\""));
        let p = Probe::memory();
        p.samples[0].count.store(1, SeqCst);
        p.in_flight.store(1, SeqCst);
        let text = p.snapshot(false, false);
        assert!(text.contains("\"registered\":false,\"sealed\":false"));
        assert!(text.contains("\"state\":\"interrupted\",\"seq\":null"));
        assert!(text.contains("\"state\":\"missing\""));
    }
}

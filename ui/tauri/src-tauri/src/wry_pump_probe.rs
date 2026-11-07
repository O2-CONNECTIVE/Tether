//! Last-published brackets only. No OS calls, identity storage, locks, retries or I/O.
use std::sync::atomic::{AtomicU8, AtomicU64, Ordering::SeqCst};
const BUSY: u8 = 1;
const LOSS: u8 = 2;
const CLOSED: u64 = 1 << 63;
const INGRESS: u64 = CLOSED - 1;
const WIDTH: usize = 23; // ten 2-bit counters plus an independent 3-bit state
const ROW: u64 = (1 << WIDTH) - 1;
const FROZEN: u64 = 1 << 63;
const REGISTERED: u64 = 1 << 46;
const REQUESTED: u64 = 1 << 47;
const SEALED: u64 = 1 << 48;
const LOST: u64 = 1 << 49;
const STATES: [&str; 6] = [
    "missing",
    "between-calls",
    "get-message-bracket",
    "dispatch-message-bracket",
    "wait-ended",
    "unknown",
];
const COUNTS: [&str; 10] = [
    "scopeEntriesCapped",
    "waitEntriesCapped",
    "waitDropsCapped",
    "scopeDropsCapped",
    "getEntriesCapped",
    "getErrorReturnsCapped",
    "getQuitReturnsCapped",
    "getOtherReturnsCapped",
    "dispatchEntriesCapped",
    "dispatchReturnsCapped",
];
#[derive(Default)]
pub(super) struct Summary {
    ingress: AtomicU64,
    gate: AtomicU8,
    live: AtomicU64,
    frozen: AtomicU64,
}
struct Flight<'a>(&'a AtomicU64);
impl Drop for Flight<'_> {
    fn drop(&mut self) {
        self.0.fetch_sub(1, SeqCst);
    }
}
pub(super) struct Writer<'a>(&'a Summary, Flight<'a>);
impl Drop for Writer<'_> {
    fn drop(&mut self) {
        self.0.gate.fetch_and(!BUSY, SeqCst);
    }
}
fn count(row: u64, index: usize) -> u64 {
    (row >> (index * 2)) & 3
}
fn increment(row: &mut u64, index: usize) {
    let shift = index * 2;
    let value = (count(*row, index) + 1).min(2);
    *row = (*row & !(3 << shift)) | (value << shift);
}
fn state(row: u64) -> u8 {
    (row >> 20) as u8
}
fn transition(row: &mut u64, value: u8) {
    *row = (*row & ((1 << 20) - 1)) | (u64::from(value) << 20);
}
impl Summary {
    // First action covers validation entrance, including losers. The cap bounds
    // admitted work, not this raw count: each live ingress owns a same-process
    // stack frame, so Windows x64 user address space cannot contain 2^63 live guards.
    // No loop, retry, waiting, allocation, or blocked pump API under this flight.
    pub(super) fn writer(&self) -> Option<Writer<'_>> {
        let entered = self.ingress.fetch_add(1, SeqCst);
        let flight = Flight(&self.ingress);
        if entered & CLOSED != 0 {
            return None;
        }
        if entered >= 32 {
            self.gate.fetch_or(LOSS, SeqCst);
            return None;
        }
        if self.gate.compare_exchange(0, BUSY, SeqCst, SeqCst).is_err() {
            self.gate.fetch_or(LOSS, SeqCst);
            return None;
        }
        let writer = Writer(self, flight);
        if self.ingress.load(SeqCst) & CLOSED != 0 {
            return None;
        }
        Some(writer)
    }
    pub(super) fn snapshot(&self, registered: bool, requested: bool) -> String {
        let existing = self.frozen.load(SeqCst);
        let frozen = if existing != 0 {
            existing
        } else {
            // Atomic close captures the complete pre-close ingress, even if a
            // paused writer finishes immediately afterward. No sampling gap.
            let closed = self.ingress.fetch_or(CLOSED, SeqCst);
            let active = closed & INGRESS != 0;
            let interrupted =
                active || closed & CLOSED != 0 || self.gate.load(SeqCst) & (BUSY | LOSS) != 0;
            // Unknown snapshots never serialize partially written histories.
            let rows = if interrupted {
                0
            } else {
                self.live.load(SeqCst)
            };
            let value = FROZEN
                | rows
                | if registered { REGISTERED } else { 0 }
                | if requested { REQUESTED } else { 0 }
                | if requested && !active { SEALED } else { 0 }
                | if interrupted { LOST } else { 0 };
            match self.frozen.compare_exchange(0, value, SeqCst, SeqCst) {
                Ok(_) => value,
                Err(first) => first,
            }
        };
        let coherent =
            frozen & (REGISTERED | REQUESTED | SEALED | LOST) == REGISTERED | REQUESTED | SEALED;
        let slots = (0..2)
            .map(|i| {
                let row = (frozen >> (i * WIDTH)) & ROW;
                let current = if coherent { state(row) } else { 5 };
                let relation = if coherent && (1..=4).contains(&current) {
                    "original-wait"
                } else {
                    "unknown"
                };
                let counts = COUNTS
                    .iter()
                    .enumerate()
                    .map(|(j, name)| format!("\"{name}\":{}", count(row, j)))
                    .collect::<Vec<_>>()
                    .join(",");
                format!(
                    "{{\"slot\":\"{}\",\"relation\":\"{relation}\",\"state\":\"{}\",{counts}}}",
                    ["environment", "controller"][i],
                    STATES[current as usize]
                )
            })
            .collect::<Vec<_>>()
            .join(",");
        format!(
            "{{\"schema\":1,\"variant\":\"wry-pump-brackets-1\",\"purpose\":\"diagnostic-hints-only\",\"registered\":{},\"sealRequested\":{},\"sealed\":{},\"lossDetected\":{},\"slots\":[{slots}]}}",
            frozen & REGISTERED != 0,
            frozen & REQUESTED != 0,
            frozen & SEALED != 0,
            frozen & LOST != 0
        )
    }
}
impl Writer<'_> {
    fn reject(&self) {
        self.0.gate.fetch_or(LOSS, SeqCst);
    }
    fn update(&self, index: usize, change: impl FnOnce(&mut u64) -> bool) {
        if self.0.ingress.load(SeqCst) & CLOSED != 0 || self.0.gate.load(SeqCst) & LOSS != 0 {
            return;
        }
        let before = self.0.live.load(SeqCst);
        let mut row = (before >> (index * WIDTH)) & ROW;
        if !change(&mut row) {
            self.reject();
            return;
        }
        // Close may race this store. BUSY stays set through it, so that close
        // freezes unknown; later stores cannot change the frozen packed word.
        self.0.live.store(
            (before & !(ROW << (index * WIDTH))) | (row << (index * WIDTH)),
            SeqCst,
        );
    }
    pub(super) fn begin(&self, kind: u8, token: u64) {
        if !(1..=2).contains(&kind) || token != u64::from(kind) {
            self.reject();
            return;
        }
        let before = self.0.live.load(SeqCst);
        if (0..2).any(|i| {
            let r = (before >> (i * WIDTH)) & ROW;
            count(r, 0) != 0 && count(r, 3) == 0
        }) {
            self.reject();
            return;
        }
        self.update(kind as usize - 1, |row| {
            if count(*row, 0) != 0 {
                return false;
            }
            increment(row, 0);
            true
        });
    }
    pub(super) fn event(
        &self,
        token: u64,
        code: u8,
        context: u8,
        owner: impl FnOnce(usize) -> bool,
    ) {
        if !(1..=2).contains(&token)
            || context & !0x3f != 0
            || context & 7 > 3
            || context & 16 == 0
            || context & 32 != 0
            || !owner(token as usize - 1)
        {
            self.reject();
            return;
        }
        self.update(token as usize - 1, |row| {
            if count(*row, 0) != 1 || count(*row, 3) != 0 {
                return false;
            }
            match code {
                1 | 2 | 5..=8 => true,
                3 if count(*row, 1) == 0 && context & 8 != 0 => {
                    increment(row, 1);
                    transition(row, 1);
                    true
                }
                4 if count(*row, 1) == 1
                    && count(*row, 2) == 0
                    && state(*row) == 1
                    && context & 8 != 0 =>
                {
                    increment(row, 2);
                    transition(row, 4);
                    true
                }
                9 if count(*row, 1) == count(*row, 2) => {
                    increment(row, 3);
                    true
                }
                _ => false,
            }
        });
    }
    fn active_target(&self) -> bool {
        let rows = self.0.live.load(SeqCst);
        (0..2).any(|i| {
            let row = (rows >> (i * WIDTH)) & ROW;
            count(row, 0) == 1 && count(row, 3) == 0
        })
    }
    pub(super) fn receiver(&self, token: u64, bound: bool, owner: impl FnOnce(usize) -> bool) {
        // Ignore only genuinely unbound script waits outside all target lifetimes.
        // Nonzero stale/invalid tokens remain errors even after scope-drop.
        if token == 0 && !bound && !self.active_target() {
            return;
        }
        if !bound || !(1..=2).contains(&token) || !owner(token as usize - 1) {
            self.reject();
            return;
        }
        self.update(token as usize - 1, |row| {
            count(*row, 0) == 1
                && count(*row, 1) == 1
                && count(*row, 2) == 0
                && count(*row, 3) == 0
                && state(*row) == 1
        });
    }
    pub(super) fn bracket(
        &self,
        token: u64,
        bound: bool,
        event: u8,
        owner: impl FnOnce(usize) -> bool,
    ) {
        if token == 0 && !bound && !self.active_target() {
            return;
        }
        if !bound || !(1..=2).contains(&token) || event > 5 || !owner(token as usize - 1) {
            self.reject();
            return;
        }
        self.update(token as usize - 1, |row| {
            if count(*row, 0) != 1
                || count(*row, 1) != 1
                || count(*row, 2) != 0
                || count(*row, 3) != 0
            {
                return false;
            }
            let (expected, next, counter) = match event {
                0 => (1, 2, 4),
                1..=3 => (2, 1, event as usize + 4),
                4 => (1, 3, 8),
                5 => (3, 1, 9),
                _ => unreachable!(),
            };
            if state(*row) != expected {
                return false;
            }
            increment(row, counter);
            transition(row, next);
            true
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn waiting() -> Summary {
        let p = Summary::default();
        {
            let w = p.writer().unwrap();
            w.begin(1, 1);
            w.event(1, 3, 24, |_| true);
        }
        p
    }
    fn unknown(s: &str) {
        assert!(!s.contains("\"relation\":\"original-wait\""));
        assert_eq!(s.matches("\"state\":\"unknown\"").count(), 2);
    }
    #[test]
    fn admission_cap_collision_and_injected_overflow_fail_closed() {
        for count in [1, 32, u64::MAX] {
            let p = waiting();
            p.ingress.store(count, SeqCst);
            if count == 1 {
                p.gate.store(BUSY, SeqCst);
            }
            assert!(p.writer().is_none());
            let first = p.snapshot(true, true);
            unknown(&first);
            p.ingress.store(CLOSED, SeqCst);
            p.gate.store(0, SeqCst);
            assert_eq!(first, p.snapshot(true, true));
        }
    }
    #[test]
    fn seal_before_validation_during_fields_and_before_drop_is_frozen() {
        for stage in 0..3 {
            let p = waiting();
            let w = p.writer().unwrap();
            let mut first = String::new();
            if stage == 0 {
                w.bracket(1, true, 0, |_| {
                    first = p.snapshot(true, true);
                    true
                });
            } else if stage == 1 {
                w.update(0, |row| {
                    increment(row, 4);
                    first = p.snapshot(true, true);
                    transition(row, 2);
                    true
                });
            } else {
                w.bracket(1, true, 0, |_| true);
                first = p.snapshot(true, true);
            }
            unknown(&first);
            drop(w);
            assert_eq!(first, p.snapshot(true, true));
            assert!(p.writer().is_none());
            assert_eq!(first, p.snapshot(false, false));
        }
        let p = waiting();
        p.ingress.store(1, SeqCst); // The atomic entrance is visible before busy admission.
        let first = p.snapshot(true, true);
        unknown(&first);
        p.ingress.store(0, SeqCst);
        assert_eq!(first, p.snapshot(true, true));
    }
    #[test]
    fn concurrent_validation_and_snapshot_never_upgrade() {
        use std::sync::{Arc, Barrier};
        for stage in 0..3 {
            let p = Arc::new(waiting());
            let entered = Arc::new(Barrier::new(2));
            let release = Arc::new(Barrier::new(2));
            let (other, ready, resume) = (p.clone(), entered.clone(), release.clone());
            let worker = std::thread::spawn(move || {
                let w = other.writer().unwrap();
                let pause = || {
                    ready.wait();
                    resume.wait();
                };
                if stage == 0 {
                    w.bracket(1, true, 0, |_| {
                        pause();
                        true
                    });
                } else if stage == 1 {
                    w.update(0, |row| {
                        increment(row, 4);
                        pause();
                        transition(row, 2);
                        true
                    });
                } else {
                    w.bracket(1, true, 0, |_| true);
                    pause();
                }
            });
            entered.wait();
            let first = p.snapshot(true, true);
            unknown(&first);
            release.wait();
            worker.join().unwrap();
            assert_eq!(first, p.snapshot(true, true));
        }
        for _ in 0..16 {
            let p = Arc::new(waiting());
            let start = Arc::new(Barrier::new(3));
            let joins: Vec<_> = (0..2)
                .map(|_| {
                    let (p, start) = (p.clone(), start.clone());
                    std::thread::spawn(move || {
                        start.wait();
                        p.snapshot(true, true)
                    })
                })
                .collect();
            start.wait();
            for join in joins {
                assert_eq!(join.join().unwrap(), p.snapshot(true, true));
            }
        }
    }

    #[test]
    fn complete_history_caps_but_state_is_explicit() {
        let p = waiting();
        for _ in 0..5 {
            let w = p.writer().unwrap();
            for event in [0, 3, 4, 5] {
                w.bracket(1, true, event, |_| true);
            }
        }
        {
            let w = p.writer().unwrap();
            w.bracket(1, true, 0, |_| true);
        }
        let s = p.snapshot(true, true);
        assert!(s.contains("\"state\":\"get-message-bracket\""));
        for name in [
            "getEntriesCapped",
            "getOtherReturnsCapped",
            "dispatchEntriesCapped",
            "dispatchReturnsCapped",
        ] {
            assert!(s.contains(&format!("\"{name}\":2")));
        }
        assert!(!s.contains("owner") && !s.contains("token"));
    }
    #[test]
    fn missing_invalid_foreign_old_scope_and_duplicate_wait_reject() {
        assert!(
            Summary::default()
                .snapshot(true, true)
                .contains("\"state\":\"missing\"")
        );
        for bad in 0..8 {
            let p = waiting();
            {
                let w = p.writer().unwrap();
                match bad {
                    0 => w.event(1, 3, 24, |_| true),
                    1 => w.event(1, 10, 48, |_| true),
                    2 => w.bracket(0, true, 0, |_| true),
                    3 => w.bracket(1, false, 0, |_| true),
                    4 => w.bracket(1, true, 0, |_| false),
                    5 => {
                        w.event(1, 4, 24, |_| true);
                        w.event(1, 9, 16, |_| true);
                        w.bracket(1, true, 0, |_| true);
                    }
                    6 => w.begin(2, 2),
                    _ => w.begin(1, 0),
                }
            }
            unknown(&p.snapshot(true, true));
        }
    }
}

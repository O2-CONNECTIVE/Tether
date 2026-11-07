//! Finite receiver-poll observations, never a continuous sender-liveness claim.
use std::sync::atomic::{AtomicBool, AtomicU8, AtomicUsize, Ordering::SeqCst};
#[derive(Default)]
pub(super) struct Summary {
    counts: [[AtomicU8; 3]; 3],
    busy: AtomicBool,
    in_flight: AtomicUsize,
    sealed: AtomicBool,
    loss: AtomicBool,
}
struct Flight<'a>(&'a AtomicUsize);
impl Drop for Flight<'_> {
    fn drop(&mut self) {
        self.0.fetch_sub(1, SeqCst);
    }
}
impl Summary {
    pub(super) fn observe(&self, bucket: usize, state: u8) {
        self.in_flight.fetch_add(1, SeqCst);
        let _flight = Flight(&self.in_flight);
        if self.sealed.load(SeqCst) {
            return;
        }
        if self
            .busy
            .compare_exchange(false, true, SeqCst, SeqCst)
            .is_err()
        {
            self.loss.store(true, SeqCst);
            return;
        }
        if !self.sealed.load(SeqCst) {
            if bucket < 3 && state < 3 {
                let c = &self.counts[bucket][state as usize];
                c.store(c.load(SeqCst).saturating_add(1).min(2), SeqCst);
            } else {
                self.loss.store(true, SeqCst);
            }
        }
        self.busy.store(false, SeqCst);
    }
    pub(super) fn snapshot(
        &self,
        registered: bool,
        requested: bool,
        ambiguous: impl FnOnce() -> [bool; 2],
    ) -> String {
        self.sealed.store(true, SeqCst);
        let busy = self.in_flight.load(SeqCst) != 0;
        let ambiguous = ambiguous();
        if busy {
            self.loss.store(true, SeqCst);
        }
        let rows = self.counts.iter().enumerate().map(|(i, counts)| {
            let relation = if registered && requested && !busy && !self.loss.load(SeqCst)
                && i < 2 && !ambiguous[i] && counts.iter().any(|c| c.load(SeqCst) != 0) { "original-wait" } else { "unknown" };
            format!("{{\"bucket\":\"{}\",\"relation\":\"{}\",\"readyCapped\":{},\"emptyCapped\":{},\"disconnectedCapped\":{}}}",
                ["environment", "controller", "unbound"][i], relation,
                counts[0].load(SeqCst), counts[1].load(SeqCst), counts[2].load(SeqCst))
        }).collect::<Vec<_>>();
        format!(
            "{{\"schema\":1,\"variant\":\"wry-receiver-polls-1\",\"purpose\":\"diagnostic-hints-only\",\"registered\":{},\"sealed\":{},\"lossDetected\":{},\"buckets\":[{}]}}",
            registered,
            requested && !busy,
            self.loss.load(SeqCst),
            rows.join(",")
        )
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn capped_and_sealed() {
        let p = Summary::default();
        for _ in 0..10 {
            p.observe(0, 1);
        }
        let s = p.snapshot(true, true, || [false; 2]);
        assert!(s.contains("\"emptyCapped\":2"));
        assert!(s.contains("\"disconnectedCapped\":0"));
        p.observe(0, 2);
        assert_eq!(s, p.snapshot(true, true, || [false; 2]));
    }
    #[test]
    fn seal_excludes_late_entries_and_marks_preexisting_flight() {
        let p = Summary::default(); p.observe(0, 1);
        let s = p.snapshot(true, true, || { p.observe(0, 2); [false; 2] });
        assert!(s.contains("\"disconnectedCapped\":0"));
        assert!(s.contains("\"lossDetected\":false"));
        let p = Summary::default(); p.observe(0, 1);
        p.in_flight.store(1, SeqCst); // Entered before seal, before any payload write.
        let s = p.snapshot(true, true, || [false; 2]);
        assert!(s.contains("\"sealed\":false"));
        assert!(s.contains("\"lossDetected\":true"));
        assert!(!s.contains("\"relation\":\"original-wait\""));
    }

    #[test]
    fn loss_partial_unknown() {
        let p = Summary::default();
        p.observe(2, 1);
        p.busy.store(true, SeqCst);
        p.in_flight.store(1, SeqCst);
        p.observe(0, 2);
        let s = p.snapshot(false, false, || [true; 2]);
        assert!(s.contains("\"lossDetected\":true"));
        assert!(s.contains("\"sealed\":false"));
        assert_eq!(s.matches("\"relation\":\"unknown\"").count(), 3);
    }
}

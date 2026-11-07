//! Companion to the unchanged delivery schema. Existing sample seq is its ordinal
//! with the separately disclosed anchor reservation removed, not a second clock.
use super::*;

#[derive(Default)]
pub(super) struct Anchor {
    attempted: AtomicBool,
    reserved_seq: AtomicU8,
    pub(super) ready_seq: AtomicU8,
    elapsed: AtomicU64,
    binding: OnceLock<(String, String)>,
    #[cfg(test)]
    after_capture: std::sync::Mutex<Option<Box<dyn FnOnce() + Send>>>,
    #[cfg(test)]
    before_publish: std::sync::Mutex<Option<Box<dyn FnOnce() + Send>>>,
    #[cfg(test)]
    before_sequence_cas: std::sync::Mutex<Option<Box<dyn FnOnce() + Send>>>,
}
impl Anchor {
    pub(super) fn project(&self, seq: u8) -> u8 {
        let anchor = self.reserved_seq.load(SeqCst);
        seq - u8::from(anchor != 0 && seq > anchor)
    }
    #[cfg(test)]
    pub(super) fn test_before_sequence_cas(&self) {
        let hook = self.before_sequence_cas.lock().unwrap().take();
        if let Some(hook) = hook {
            hook();
        }
    }
    #[cfg(test)]
    pub(super) fn test_after_capture(&self) {
        let hook = self.after_capture.lock().unwrap().take();
        if let Some(hook) = hook {
            hook();
        }
    }
}
fn digest(value: &str) -> bool {
    value.len() == 64
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
impl Probe {
    /// Collector only. Trusted-job binding is validated by the signal reader;
    /// these are echoed hashes, not authentication or Host-computed file hashes.
    pub(in super::super) fn try_anchor(&self, manifest_sha256: &str, failure_sha256: &str) -> bool {
        let Some(_flight) = self.flight() else {
            return false;
        };
        if self.anchor.attempted.swap(true, SeqCst)
            || !digest(manifest_sha256)
            || !digest(failure_sha256)
        {
            self.loss.store(true, SeqCst);
            return false;
        }
        let seq = self.sequence.load(SeqCst);
        #[cfg(test)]
        self.anchor.test_before_sequence_cas();
        if seq >= 7
            || self
                .sequence
                .compare_exchange(seq, seq + 1, SeqCst, SeqCst)
                .is_err()
        {
            self.loss.store(true, SeqCst);
            return false;
        }
        self.anchor.reserved_seq.store(seq + 1, SeqCst);
        let elapsed = self.start.elapsed().as_micros();
        if elapsed > (1_u128 << 53) - 1 {
            self.loss.store(true, SeqCst);
            return false;
        }
        self.anchor.elapsed.store(elapsed as u64, SeqCst);
        if self
            .anchor
            .binding
            .set((manifest_sha256.into(), failure_sha256.into()))
            .is_err()
        {
            self.loss.store(true, SeqCst);
            return false;
        }
        #[cfg(test)]
        {
            let hook = self.anchor.before_publish.lock().unwrap().take();
            if let Some(hook) = hook {
                hook();
            }
        }
        if !self.enabled.load(SeqCst) {
            self.loss.store(true, SeqCst);
            return false;
        }
        // Metadata is immutable and complete before this SeqCst release publication.
        self.anchor.ready_seq.store(seq + 1, SeqCst);
        true
    }
    /// Call after original snapshot. No reopening or extension of the collector.
    pub(in super::super) fn anchor_snapshot(&self, delivery: &str) -> String {
        let seq = self.anchor.reserved_seq.load(SeqCst);
        let ready = seq != 0 && self.anchor.ready_seq.load(SeqCst) == seq;
        let state = if ready {
            "observed"
        } else if self.anchor.attempted.load(SeqCst) {
            "interrupted"
        } else {
            "missing"
        };
        let binding = if ready {
            self.anchor.binding.get()
        } else {
            None
        };
        let (manifest, failure) = binding.map_or(("null".into(), "null".into()), |(m, f)| {
            (format!("\"{m}\""), format!("\"{f}\""))
        });
        let anchor = format!(
            "{{\"state\":\"{}\",\"seq\":{},\"elapsedUs\":{},\"manifestSha256\":{},\"failureSha256\":{}}}",
            state,
            if seq == 0 {
                "null".into()
            } else {
                seq.to_string()
            },
            if ready {
                self.anchor.elapsed.load(SeqCst).to_string()
            } else {
                "null".into()
            },
            manifest,
            failure
        );
        let samples = self
            .samples
            .iter()
            .map(|s| {
                let observed = s.count.load(SeqCst) == 1 && s.ready.load(SeqCst);
                format!(
                    "{{\"seq\":{},\"anchorSeqObserved\":{}}}",
                    if observed {
                        s.seq.load(SeqCst).to_string()
                    } else {
                        "null".into()
                    },
                    if observed {
                        s.anchor_seq_observed.load(SeqCst)
                    } else {
                        0
                    }
                )
            })
            .collect::<Vec<_>>();
        format!(
            "{{\"schema\":1,\"variant\":\"wry-sdk-post-verdict-1\",\"purpose\":\"diagnostic-hints-only\",\"anchor\":{},\"samples\":[{}],\"delivery\":{}}}",
            anchor,
            samples.join(","),
            delivery
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn bind(p: &Probe) -> bool {
        p.try_anchor(&"a".repeat(64), &"b".repeat(64))
    }
    #[test]
    fn zero_captured_before_publication_is_never_retroactive() {
        let p = Probe::memory();
        let token = p.begin(2);
        let other = p.clone();
        *p.anchor.after_capture.lock().unwrap() = Some(Box::new(move || assert!(bind(&other))));
        p.event(token, 5, 17);
        assert_eq!(p.anchor.ready_seq.load(SeqCst), 1);
        assert_eq!(p.samples[3].seq.load(SeqCst), 2);
        assert_eq!(p.samples[3].anchor_seq_observed.load(SeqCst), 0);
    }
    #[test]
    fn metadata_before_release_is_not_a_published_anchor() {
        let p = Probe::memory();
        let token = p.begin(2);
        let other = p.clone();
        *p.anchor.before_publish.lock().unwrap() =
            Some(Box::new(move || other.event(token, 5, 17)));
        assert!(bind(&p));
        assert_eq!(p.samples[3].seq.load(SeqCst), 2);
        assert_eq!(p.samples[3].anchor_seq_observed.load(SeqCst), 0);
        assert_eq!(p.anchor.ready_seq.load(SeqCst), 1);
    }
    #[test]
    fn single_shot_sequence_cas_loss_is_not_retried() {
        for anchor_loses in [true, false] {
            let p = Probe::memory();
            let token = p.begin(2);
            let other = p.clone();
            if anchor_loses {
                *p.anchor.before_sequence_cas.lock().unwrap() =
                    Some(Box::new(move || other.event(token, 5, 17)));
                assert!(!bind(&p));
                assert_eq!(p.anchor.ready_seq.load(SeqCst), 0);
            } else {
                *p.anchor.before_sequence_cas.lock().unwrap() =
                    Some(Box::new(move || assert!(bind(&other))));
                p.event(token, 5, 17);
                assert!(!p.samples[3].ready.load(SeqCst));
            }
            assert!(p.loss.load(SeqCst));
            assert_eq!(p.sequence.load(SeqCst), 1);
        }
    }
    #[test]
    fn seal_during_metadata_cannot_publish_and_flight_loss_is_closed() {
        let p = Probe::memory();
        let other = p.clone();
        *p.anchor.before_publish.lock().unwrap() = Some(Box::new(move || {
            let _ = other.snapshot(true, true);
        }));
        assert!(!bind(&p));
        assert!(p.loss.load(SeqCst));
        assert_eq!(p.anchor.ready_seq.load(SeqCst), 0);
        let p = Probe::memory();
        p.in_flight.store(32, SeqCst);
        assert!(!bind(&p));
        assert!(p.loss.load(SeqCst));
    }
    #[test]
    fn expiry_or_finish_seal_and_invalid_or_duplicate_anchor_are_closed() {
        let p = Probe::memory();
        let _ = p.snapshot(true, true);
        assert!(!bind(&p));
        assert_eq!(p.sequence.load(SeqCst), 0);
        let p = Probe::memory();
        assert!(!p.try_anchor("invalid", &"b".repeat(64)));
        assert!(p.loss.load(SeqCst));
        assert_eq!(p.anchor.ready_seq.load(SeqCst), 0);
        let p = Probe::memory();
        assert!(bind(&p));
        assert!(!bind(&p));
        assert!(p.loss.load(SeqCst));
        assert_eq!(p.sequence.load(SeqCst), 1);
    }
}

use super::decoder::{decode, Frame};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum FeedFault {
    Unavailable,
    Stale,
    Malformed,
    Clock,
    Sequence,
    Eof,
    Read,
    Rate,
    Stopped,
}

impl FeedFault {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::Unavailable => "unavailable",
            Self::Stale => "stale",
            Self::Malformed => "malformed",
            Self::Clock => "clock",
            Self::Sequence => "sequence",
            Self::Eof => "eof",
            Self::Read => "read",
            Self::Rate => "rate",
            Self::Stopped => "stopped",
        }
    }
}

// This is a Mac INPUT observation, not HostStatusPresentation or authority.
#[derive(Debug, Clone)]
pub(crate) struct Snapshot {
    pub(crate) frame: Option<Frame>,
    pub(crate) fault: Option<FeedFault>,
    pub(crate) received: f64,
    pub(crate) source_time: f64,
    pub(crate) remaining_deadline: Option<f64>,
    pub(crate) sticky_unknown: bool,
}

impl Default for Snapshot {
    fn default() -> Self {
        Self {
            frame: None,
            fault: Some(FeedFault::Unavailable),
            received: 0.0,
            source_time: 0.0,
            remaining_deadline: None,
            sticky_unknown: false,
        }
    }
}

impl Snapshot {
    pub(crate) fn source_closed(&self) -> bool {
        self.fault == Some(FeedFault::Eof)
    }

    pub(crate) fn terminal_observed(&self) -> bool {
        self.frame.as_ref().is_some_and(|frame| frame.terminal)
    }

    pub(crate) fn fresh(&self, now: f64) -> bool {
        self.fault.is_none()
            && self.frame.is_some()
            && now >= self.received
            && now >= self.source_time
            && now - self.source_time <= 3.0
    }

    // Seconds rounded up, exactly like Swift Snapshot.remaining(at:).
    pub(crate) fn remaining(&self, now: f64) -> Option<u64> {
        if !self.fresh(now) {
            return None;
        }
        self.remaining_deadline
            .map(|deadline| (deadline - now).ceil().max(0.0) as u64)
    }
}

// One instance owns one feed generation. There is no wire generation field,
// reset/reconnect operation, authentication or permission in this state.
#[derive(Debug, Default)]
pub(crate) struct FeedState {
    snapshot: Snapshot,
    poisoned: bool,
    last_seq: u64,
    last_time: f64,
    last_source: f64,
}

impl FeedState {
    pub(crate) fn snapshot(&self) -> &Snapshot {
        &self.snapshot
    }

    // Match Swift: fail poisons ingestion but a later explicit fail may replace
    // the fault label. It never clears retained frame/cleanup/unknown history.
    pub(crate) fn fail(&mut self, fault: FeedFault) {
        self.snapshot.fault = Some(fault);
        self.poisoned = true;
    }

    pub(crate) fn ingest(&mut self, bytes: &[u8], now: f64) {
        if self.poisoned {
            return;
        }
        if !now.is_finite() || now < self.last_time {
            self.fail(FeedFault::Clock);
            return;
        }
        let frame = match decode(bytes) {
            Ok(frame) => frame,
            Err(_) => {
                self.fail(FeedFault::Malformed);
                return;
            }
        };
        if frame.seq <= self.last_seq || self.snapshot.terminal_observed() {
            self.fail(FeedFault::Sequence);
            return;
        }
        let source = frame.source_ms as f64 / 1000.0;
        if source > now || source < self.last_source {
            self.fail(FeedFault::Clock);
            return;
        }
        if now - source > 3.0 {
            self.fail(FeedFault::Stale);
            return;
        }
        if let Some(milliseconds) = frame.remaining {
            let candidate = source + f64::from(milliseconds) / 1000.0;
            self.snapshot.remaining_deadline = Some(
                self.snapshot
                    .remaining_deadline
                    .map_or(candidate, |prior| prior.min(candidate)),
            );
        }
        self.snapshot.sticky_unknown |= frame.terminal_unknown;
        self.last_seq = frame.seq;
        self.last_time = now;
        self.last_source = source;
        self.snapshot.frame = Some(frame);
        self.snapshot.fault = None;
        self.snapshot.received = now;
        self.snapshot.source_time = source;
    }
}

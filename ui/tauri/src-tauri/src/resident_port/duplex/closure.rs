//! Private post-Close evidence. Never an ordinary OFF observation or IPC field.
use super::wire::{CloseOutcome, Event};
#[derive(Default)]
pub(super) struct Closure {
    requested: Option<u64>,
    half_closed: bool,
    terminal: Option<CloseOutcome>,
    failed: bool,
    eof: bool,
    last_now: u64,
    last_source: Option<u64>,
}
impl Closure {
    pub(super) fn seed_source(&mut self, source: Option<u64>) {
        if !self.requested() {
            self.last_source = source;
        }
    }
    pub(super) fn tick(&mut self, now: u64) -> Result<(), ()> {
        if now < self.last_now {
            self.failed = true;
        }
        self.last_now = self.last_now.max(now);
        if self.failed {
            Err(())
        } else {
            Ok(())
        }
    }
    fn source(&mut self, timestamp: u64, now: u64) -> Result<(), ()> {
        if timestamp > now || self.last_source.is_some_and(|last| timestamp < last) {
            return Err(());
        }
        self.last_source = Some(timestamp);
        Ok(())
    }
    pub(super) fn request(&mut self, now: u64) {
        let _ = self.tick(now);
        if self.requested.is_none() {
            self.requested = Some(now);
        }
    }
    pub(super) fn requested(&self) -> bool {
        self.requested.is_some()
    }
    pub(super) fn half_closed(&mut self) {
        self.half_closed = true;
    }
    pub(super) fn invalidate(&mut self) {
        self.failed = true;
    }
    pub(super) fn eof(&mut self) {
        self.eof = true;
    }
    pub(super) fn confirmed(&self) -> bool {
        self.requested()
            && self.half_closed
            && self.eof
            && !self.failed
            && self.terminal == Some(CloseOutcome::Confirmed)
    }
    pub(super) fn event(&mut self, event: Event, now: u64) -> Result<(), ()> {
        let result = self.tick(now).and_then(|()| self.accept(event, now));
        if result.is_err() {
            self.failed = true;
        }
        result
    }
    fn accept(&mut self, event: Event, now: u64) -> Result<(), ()> {
        if !self.half_closed || self.failed || self.eof || self.terminal.is_some() {
            return Err(());
        }
        match event {
            Event::Closed {
                source_continuous_ms,
                outcome,
                ..
            } => {
                if self
                    .requested
                    .is_none_or(|start| source_continuous_ms < start)
                    || source_continuous_ms > now
                    || now - source_continuous_ms >= 3000
                {
                    return Err(());
                }
                self.source(source_continuous_ms, now)?;
                self.terminal = Some(outcome);
                if outcome == CloseOutcome::Unknown {
                    self.failed = true;
                }
            }
            // These ordinary observations cannot renew shutdown or supply Close
            // proof. Validate shape/invariants, but not their 3s freshness: actual
            // Close may take 75s. The proof reader has no overall 3s timeout.
            Event::Status {
                snapshot,
                source_continuous_ms,
                ..
            } => {
                snapshot.observation()?;
                self.source(source_continuous_ms, now)?;
            }
            Event::Receipt { .. } => {}
            Event::StartupRejected { .. } => return Err(()),
        }
        Ok(())
    }
}

//! Private pre-owner failure proof, never OFF or ordinary cleanup confirmation.
use super::wire::Event;
#[derive(Default)]
pub(super) struct Startup {
    seen_frame: bool,
    attempted_intent: bool,
    rejected: bool,
    eof: bool,
    failed: bool,
    last_now: u64,
}
impl Startup {
    pub(super) fn tick(&mut self, now: u64) -> Result<(), ()> {
        self.failed |= now < self.last_now;
        self.last_now = self.last_now.max(now);
        if self.failed {
            Err(())
        } else {
            Ok(())
        }
    }
    pub(super) fn intent(&mut self) {
        self.attempted_intent = true;
        if self.rejected {
            self.failed = true;
        }
    }
    pub(super) fn received(&self) -> bool {
        self.rejected
    }
    pub(super) fn fault(&mut self) {
        self.failed = true;
    }
    pub(super) fn eof(&mut self) {
        self.eof = true;
    }
    pub(super) fn confirmed(&self) -> bool {
        self.rejected && self.eof && !self.failed && !self.attempted_intent
    }
    pub(super) fn event(&mut self, event: &Event, now: u64) -> Result<bool, ()> {
        self.tick(now)?;
        if self.rejected {
            self.failed = true;
            return Err(());
        }
        let first = !self.seen_frame;
        self.seen_frame = true;
        if let Event::StartupRejected {
            source_continuous_ms,
            ..
        } = event
        {
            if !first
                || self.attempted_intent
                || *source_continuous_ms > now
                || now - *source_continuous_ms >= 3000
            {
                self.failed = true;
                return Err(());
            }
            self.rejected = true;
            return Ok(true);
        }
        Ok(false)
    }
}

#[cfg(test)]
#[path = "startup_tests.rs"]
mod tests;

//! Private pre-owner failure proof, never OFF or ordinary cleanup confirmation.
use super::wire::{Event, RejectionCode};
#[derive(Default)]
pub(super) struct Startup {
    seen_frame: bool,
    attempted_intent: bool,
    rejection: Option<RejectionCode>,
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
        if self.rejection.is_some() {
            self.failed = true;
        }
    }
    pub(super) fn received(&self) -> bool {
        self.rejection.is_some()
    }
    pub(super) fn fault(&mut self) {
        self.failed = true;
    }
    pub(super) fn eof(&mut self) {
        self.eof = true;
    }
    pub(super) fn confirmed(&self) -> bool {
        self.rejection.is_some() && self.eof && !self.failed && !self.attempted_intent
    }
    pub(super) fn confirmed_for_exit(&self, exit_code: Option<i32>) -> Option<RejectionCode> {
        self.confirmed()
            .then_some(self.rejection)
            .flatten()
            .filter(|code| Some(code.exit_code()) == exit_code)
    }
    pub(super) fn event(&mut self, event: &Event, now: u64) -> Result<bool, ()> {
        self.tick(now)?;
        if self.rejection.is_some() {
            self.failed = true;
            return Err(());
        }
        let first = !self.seen_frame;
        self.seen_frame = true;
        if let Event::StartupRejected {
            source_continuous_ms,
            code,
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
            self.rejection = Some(*code);
            return Ok(true);
        }
        Ok(false)
    }
}

#[cfg(test)]
#[path = "startup_tests.rs"]
mod tests;

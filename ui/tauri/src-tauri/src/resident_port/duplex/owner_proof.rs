use super::*;
impl Supervisor {
    pub(super) fn record(&mut self, outcome: &io::Result<End>) {
        self.known = outcome.is_ok();
        self.cleanup_confirmed = matches!(outcome, Ok(End::Reaped(status)) if status.success())
            && self
                .shared
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .closure
                .confirmed();
        self.startup_rejected = matches!(outcome, Ok(End::Reaped(status)) if status.code() == Some(2))
            && self
                .shared
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .startup
                .confirmed();
        self.not_spawned = matches!(outcome, Ok(End::NotSpawned));
    }
    pub(super) fn can_release(&self) -> bool {
        self.known
            && (!self.require_cleanup
                || self.not_spawned
                || self.cleanup_confirmed
                || self.startup_rejected)
    }
}

use super::super::wire::RejectionCode;
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
        let exit_code = match outcome {
            Ok(End::Reaped(status)) => status.code(),
            _ => None,
        };
        let rejection = self
            .shared
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .startup
            .confirmed_for_exit(exit_code);
        self.startup_rejected = rejection == Some(RejectionCode::PolicyRejected);
        self.host_key_unavailable = rejection == Some(RejectionCode::HostKeyUnavailable);
        self.not_spawned = matches!(outcome, Ok(End::NotSpawned));
    }
    pub(super) fn can_release(&self) -> bool {
        self.known
            && (!self.require_cleanup
                || self.not_spawned
                || self.cleanup_confirmed
                || self.startup_rejected
                || self.host_key_unavailable)
    }
}

//! One producer lifetime. Neither bytes written nor process exit is authority.
use super::super::model::{Disposition, Intent, IntentRequest, Receipt};
use super::super::observation::*;
use super::wire::Event;
#[derive(Default)]
pub(super) struct Tracking {
    latest: Option<(Observation, u64)>,
    pending: Option<(u64, Intent, u64)>,
    awaiting: Option<(u64, Intent)>,
    last_request: u64,
    last_now: u64,
    last_source: Option<u64>,
    pub(super) failed: bool,
    cleanup_unknown: bool,
}
impl Tracking {
    pub(super) fn sample(&mut self, now: u64) -> bool {
        self.tick(now);
        !self.failed
    }
    pub(super) fn source_time(&self) -> Option<u64> {
        self.last_source
    }
    pub(super) fn current(&mut self, now: u64) -> Option<Observation> {
        self.tick(now);
        let (mut result, deadline) = self.latest.clone()?;
        result.valid_for_ms = if self.failed {
            0
        } else {
            deadline.saturating_sub(now).min(3000) as u32
        };
        if result.valid_for_ms == 0 || self.cleanup_unknown {
            result.can_request_on = false;
            result.can_request_off = false;
        }
        Some(result)
    }
    fn tick(&mut self, now: u64) {
        self.failed |= now < self.last_now;
        self.last_now = now;
    }
    pub(super) fn begin(&mut self, request: &IntentRequest, now: u64) -> Result<(), Disposition> {
        let current = self.current(now).ok_or(Disposition::Unavailable)?;
        if request.validate().is_err() || request.request_id <= self.last_request {
            return Err(Disposition::Stale);
        }
        self.last_request = request.request_id;
        if self.failed || current.valid_for_ms == 0 || current.revision != request.expected_revision
        {
            return Err(Disposition::Stale);
        }
        if self.pending.is_some()
            || self.awaiting.is_some()
            || !match request.intent {
                Intent::On => current.can_request_on,
                Intent::Off => current.can_request_off,
            }
        {
            return Err(Disposition::Rejected);
        }
        self.pending = Some((request.request_id, request.intent, current.revision));
        self.awaiting = Some((request.request_id, request.intent));
        Ok(())
    }
    pub(super) fn event(&mut self, event: Event, now: u64) -> Result<Option<Receipt>, ()> {
        self.tick(now);
        if self.failed {
            return Err(());
        }
        let result = match event {
            Event::Closed { .. } | Event::StartupRejected { .. } => Err(()),
            Event::Status {
                source_continuous_ms,
                snapshot,
                ..
            } => snapshot
                .observation()
                .and_then(|observation| self.status(observation, source_continuous_ms, now))
                .map(|()| None),
            Event::Receipt { receipt, .. } => {
                if self.awaiting != Some((receipt.request_id, receipt.intent)) {
                    Err(())
                } else {
                    self.awaiting = None;
                    Ok(Some(receipt))
                }
            }
        };
        self.failed |= result.is_err();
        result
    }
    fn status(&mut self, observation: Observation, timestamp: u64, now: u64) -> Result<(), ()> {
        let deadline = timestamp
            .checked_add(u64::from(observation.valid_for_ms))
            .ok_or(())?;
        if self
            .last_source
            .is_some_and(|previous| timestamp < previous)
            || timestamp > now
            || deadline < now
            || (self.cleanup_unknown && observation.cleanup != Cleanup::Unknown)
        {
            return Err(());
        }
        if let Some((previous, old_deadline)) = &self.latest {
            if observation.revision < previous.revision {
                return Err(());
            }
            if observation.revision == previous.revision {
                let mut reduced = previous.clone();
                reduced.valid_for_ms = observation.valid_for_ms;
                if reduced != observation || deadline > *old_deadline {
                    return Err(());
                }
            }
        }
        if let Some((id, intent)) = observation.unresolved() {
            if self
                .pending
                .is_some_and(|(old_id, old_intent, _)| (old_id, old_intent) != (id, intent))
            {
                return Err(());
            }
            if self.pending.is_none() {
                self.pending = Some((id, intent, observation.revision));
            }
        }
        if let Some(t) = &observation.transition {
            if self.pending.is_some_and(|(id, intent, revision)| {
                (id, intent) == (t.request_id, t.intent) && observation.revision > revision
            }) && matches!(
                t.outcome,
                TransitionOutcome::Applied | TransitionOutcome::Rejected
            ) {
                self.pending = None;
            }
        }
        self.cleanup_unknown |= observation.cleanup == Cleanup::Unknown;
        if let Some(transition) = &observation.transition {
            self.last_request = self.last_request.max(transition.request_id);
        }
        self.last_source = Some(timestamp);
        self.latest = Some((observation, deadline));
        Ok(())
    }
}

//! Bounded exchange state, separate from descriptor and child ownership.
use super::super::model::{Disposition, IntentRequest, Receipt};
use super::super::observation::Observation;
use super::{closure::Closure, tracking::Tracking, wire};
use std::sync::mpsc::{self, Receiver, SyncSender};
#[derive(Default)]
pub(super) struct Shared {
    pub(super) tracking: Tracking,
    pub(super) startup: super::startup::Startup,
    pub(super) closure: Closure,
    pub(super) output: Option<Vec<u8>>,
    pub(super) waiting: Option<(IntentRequest, SyncSender<Receipt>, u64)>,
}
impl Shared {
    pub(super) fn submit(&mut self, request: IntentRequest, now: u64) -> Receiver<Receipt> {
        self.startup.intent();
        let (send, receive) = mpsc::sync_channel(1);
        let encoded = wire::encode(&request);
        let admission = if self.closure.requested() || self.startup.received() || encoded.is_err() {
            Err(Disposition::Rejected)
        } else {
            self.tracking.begin(&request, now)
        };
        match admission {
            Err(disposition) => {
                let _ = send.try_send(Receipt::new(request, disposition));
            }
            Ok(()) => {
                self.output = Some(encoded.unwrap());
                self.waiting = Some((request, send, now.saturating_add(3000)));
            }
        }
        receive
    }
    pub(super) fn current(&mut self, now: u64) -> Option<Observation> {
        let mut status = self.tracking.current(now)?;
        if self.closure.requested() {
            status.valid_for_ms = 0;
            status.can_request_on = false;
            status.can_request_off = false;
        }
        Some(status)
    }
    pub(super) fn written(&mut self, result: std::io::Result<usize>) -> Result<(), ()> {
        let exact = self
            .output
            .as_ref()
            .is_some_and(|bytes| matches!(result, Ok(n) if n == bytes.len()));
        if !exact {
            self.fault();
            return Err(());
        }
        self.output = None;
        Ok(())
    }
    fn discard_submission(&mut self) {
        self.output = None;
        if let Some((request, send, _)) = self.waiting.take() {
            let _ = send.try_send(Receipt::new(request, Disposition::Unknown));
        }
    }
    pub(super) fn sample(&mut self, now: u64) -> Result<(), ()> {
        self.startup.tick(now)?;
        if !self.tracking.sample(now) {
            self.closure.invalidate();
            return Err(());
        }
        if self.closure.requested() {
            self.closure.tick(now)?;
        }
        Ok(())
    }
    pub(super) fn close_control(&mut self, now: u64) {
        let _ = self.startup.tick(now);
        let valid_clock = self.tracking.sample(now);
        self.closure.seed_source(self.tracking.source_time());
        self.closure.request(now);
        if !valid_clock {
            self.closure.invalidate();
        }
        self.discard_submission();
    }
    pub(super) fn fault(&mut self) {
        self.startup.fault();
        self.tracking.failed = true;
        self.closure.invalidate();
        self.discard_submission();
    }
    pub(super) fn ended(&mut self) {
        self.startup.eof();
        self.tracking.failed = true;
        self.closure.eof();
        self.discard_submission();
    }
}
#[cfg(test)]
#[path = "write_tests.rs"]
mod write_tests;

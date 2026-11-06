//! Finite, non-authoritative observations for failed owned-fixture readiness.
use super::wire::Event;
use std::io::{ErrorKind, Result};

#[derive(Debug, Default)]
pub(in crate::resident_port::duplex) struct FixtureTrace {
    reads: u64,
    pending: u64,
    bytes: u64,
    first_bytes_at: Option<u64>,
    last_read_at: Option<u64>,
    eof: bool,
    error: Option<ErrorKind>,
    frames: u64,
    first_source: Option<u64>,
    last_source: Option<u64>,
    parse_errors: u64,
}
impl FixtureTrace {
    pub(in crate::resident_port::duplex) fn read(&mut self, result: &Result<usize>, now: u64) {
        self.reads = self.reads.saturating_add(1);
        self.last_read_at = Some(now);
        match result {
            Ok(0) => self.eof = true,
            Ok(count) => {
                self.bytes = self.bytes.saturating_add(*count as u64);
                self.first_bytes_at.get_or_insert(now);
            }
            Err(error)
                if matches!(error.kind(), ErrorKind::WouldBlock | ErrorKind::Interrupted) =>
            {
                self.pending = self.pending.saturating_add(1);
            }
            Err(error) => self.error = Some(error.kind()),
        }
    }
    pub(in crate::resident_port::duplex) fn frame(&mut self, event: &Event) {
        self.frames = self.frames.saturating_add(1);
        let source = match event {
            Event::Status {
                source_continuous_ms,
                ..
            }
            | Event::Closed {
                source_continuous_ms,
                ..
            }
            | Event::StartupRejected {
                source_continuous_ms,
                ..
            } => Some(*source_continuous_ms),
            Event::Receipt { .. } => None,
        };
        if self.first_source.is_none() {
            self.first_source = source;
        }
        self.last_source = source;
    }
    pub(in crate::resident_port::duplex) fn parse_failed(&mut self) {
        self.parse_errors = self.parse_errors.saturating_add(1);
    }
}

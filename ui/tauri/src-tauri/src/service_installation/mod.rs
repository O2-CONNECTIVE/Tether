//! Read-only installation evidence. No peer, approval, or execution authority.
//! A configured owner uses one private thread. Unjoinable/uncertain native
//! ownership aborts this GUI process; it never stops the protected service.
mod generated;
mod policy;
mod retained;
#[cfg(test)]
mod tests;
#[cfg(windows)]
mod windows;
mod worker;

use std::sync::atomic::AtomicBool;
use std::time::{Duration, Instant};
use worker::Worker;

#[derive(Clone, Copy)]
pub(super) struct Seed {
    pub service_name: &'static str,
    pub service_sid: &'static str,
    pub operator_sid: &'static str,
    pub guest_sid: &'static str,
    pub record_path: &'static str,
    pub digest_path: &'static str,
    pub volume_root: &'static str,
    pub filesystem: &'static str,
    pub serial: u64,
    pub read_ms: u64,
    pub cleanup_ms: u64,
    pub record: &'static [u8],
    pub digest: &'static [u8],
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Fault {
    Denied,
    Unknown,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct Principal {
    pub user: String,
    pub kind: u32,
    pub token_id: u64,
    pub authentication_id: u64,
    pub modified_id: u64,
    pub groups: Vec<(String, u32)>,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct Ace {
    pub kind: u8,
    pub flags: u8,
    pub mask: u32,
    pub sid: String,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct FileState {
    pub path: String,
    pub owner: String,
    pub filesystem: String,
    pub volume: u64,
    pub id: [u8; 16],
    pub acl: Vec<Ace>,
    pub disk: bool,
    pub directory: bool,
    pub reparse: bool,
    pub dacl: bool,
    pub protected: bool,
    pub links: u32,
    pub size: u64,
}

pub(super) trait Calls: Send + 'static {
    fn principal(&self) -> Result<Principal, Fault>;
    fn service_sid(&self, name: &str) -> Result<String, Fault>;
    fn open_token(&self) -> Result<usize, Fault>;
    fn token_principal(&self, token: usize) -> Result<Principal, Fault>;
    fn open(&self, path: &str, leaf: bool) -> Result<usize, Fault>;
    fn inspect(&self, file: usize) -> Result<FileState, Fault>;
    fn read(
        &self,
        file: usize,
        length: usize,
        stop: &AtomicBool,
        deadline: Instant,
        cleanup: Duration,
    ) -> Result<Vec<u8>, Fault>;
    fn close(&self, handle: usize) -> Result<(), Fault>;
    fn fatal(&self) -> !;
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Observation {
    NotConfigured,
    Pending,
    Checked(u64),
    Denied,
    Unknown,
}

/// The normal caller polls this Interface, not OS handles or seed fields.
/// No successful observation enables an operation. Every poll requests a fresh
/// recheck. Checked(sequence) is a one-shot historical result, never current
/// authority; it cannot satisfy a later request or enable an operation.
pub(crate) struct Installation {
    worker: Option<Worker>,
    initial: Observation,
}
impl Default for Installation {
    fn default() -> Self {
        Self::from_generated(generated::seed())
    }
}
impl Installation {
    fn from_generated(seed: Option<Seed>) -> Self {
        // Availability tradeoff (GUI fail-stop vs retained unknown) is not yet
        // adopted. A generated seed alone MUST NOT activate this evaluator.
        const NATIVE_EVALUATION_ADMITTED: bool = false;
        if !NATIVE_EVALUATION_ADMITTED {
            return Self {
                worker: None,
                initial: Observation::NotConfigured,
            };
        }
        let Some(seed) = seed else {
            return Self {
                worker: None,
                initial: Observation::NotConfigured,
            };
        };
        #[cfg(windows)]
        {
            Self::with_calls(seed, windows::Native)
        }
        #[cfg(not(windows))]
        {
            let _ = seed;
            Self {
                worker: None,
                initial: Observation::Denied,
            }
        }
    }
}
impl Installation {
    #[cfg(any(windows, test))]
    fn with_calls(seed: Seed, calls: impl Calls) -> Self {
        if !policy::valid_seed(&seed) {
            return Self {
                worker: None,
                initial: Observation::Denied,
            };
        }
        match Worker::start(seed, calls) {
            Ok(worker) => Self {
                worker: Some(worker),
                initial: Observation::Pending,
            },
            Err(_) => Self {
                worker: None,
                initial: Observation::Unknown,
            },
        }
    }
    pub(crate) fn observe(&mut self) -> Observation {
        self.worker.as_mut().map_or(self.initial, Worker::observe)
    }
}

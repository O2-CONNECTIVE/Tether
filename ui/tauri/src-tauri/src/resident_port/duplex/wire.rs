//! Strict local process wire. Parsing is not producer authentication.
use super::super::model::{IntentRequest, Receipt, SAFE_INTEGER};
use super::super::observation::*;
use serde::{Deserialize, Serialize};
pub(super) const MAX_LINE: usize = 4095; // Plus exactly one LF: 4096-byte frame.
#[derive(Deserialize)]
#[serde(tag = "type", rename_all = "kebab-case", deny_unknown_fields)]
pub(super) enum Event {
    #[serde(rename = "startupRejected", rename_all = "camelCase")]
    StartupRejected {
        protocol_version: u8,
        source_continuous_ms: u64,
        code: RejectionCode,
    },
    #[serde(rename_all = "camelCase")]
    Status {
        protocol_version: u8,
        source_continuous_ms: u64,
        snapshot: Snapshot,
    },
    #[serde(rename_all = "camelCase")]
    Closed {
        protocol_version: u8,
        source_continuous_ms: u64,
        outcome: CloseOutcome,
    },
    Receipt {
        #[serde(rename = "protocolVersion")]
        protocol_version: u8,
        receipt: Receipt,
    },
}
#[derive(Deserialize)]
pub(super) enum RejectionCode {
    #[serde(rename = "policy-rejected")]
    PolicyRejected,
}
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub(super) enum CloseOutcome {
    Confirmed,
    Unknown,
}
#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(super) struct Snapshot {
    protocol_version: u8,
    source: Source,
    state: State,
    revision: u64,
    valid_for_ms: u32,
    standby: Standby,
    registration: Registration,
    #[serde(deserialize_with = "Option::deserialize")]
    caller: Option<String>,
    work: Work,
    cleanup: Cleanup,
    can_request_on: bool,
    can_request_off: bool,
    #[serde(deserialize_with = "Option::deserialize")]
    transition: Option<Transition>,
}
#[derive(Deserialize)]
enum Source {
    #[serde(rename = "resident-owner")]
    Owner,
}
#[derive(Deserialize)]
enum State {
    #[serde(rename = "source-observation")]
    Observation,
}
impl Snapshot {
    pub(super) fn observation(self) -> Result<Observation, ()> {
        let _ = (self.source, self.state);
        let observation = Observation {
            revision: self.revision,
            valid_for_ms: self.valid_for_ms,
            standby: self.standby,
            registration: self.registration,
            caller: self.caller,
            work: self.work,
            cleanup: self.cleanup,
            can_request_on: self.can_request_on,
            can_request_off: self.can_request_off,
            transition: self.transition,
        };
        (self.protocol_version == 1
            && observation.valid()
            && observation.transition.as_ref().is_none_or(|t| {
                t.intent != super::super::model::Intent::On
                    || t.outcome != TransitionOutcome::Applied
                    || observation.registration != Registration::Unknown
            }))
        .then_some(observation)
        .ok_or(())
    }
}
impl Event {
    pub(super) fn parse(bytes: &[u8]) -> Result<Self, ()> {
        if bytes.is_empty() || bytes.len() > MAX_LINE || bytes.contains(&b'\r') {
            return Err(());
        }
        let event: Self = serde_json::from_slice(bytes).map_err(|_| ())?;
        match &event {
            Self::Status {
                protocol_version,
                source_continuous_ms,
                ..
            }
            | Self::StartupRejected {
                protocol_version,
                source_continuous_ms,
                code: RejectionCode::PolicyRejected,
            }
            | Self::Closed {
                protocol_version,
                source_continuous_ms,
                ..
            } if *protocol_version == 1 && *source_continuous_ms <= SAFE_INTEGER => Ok(event),
            Self::Receipt {
                protocol_version,
                receipt,
            } if *protocol_version == 1
                && receipt.protocol_version == 1
                && receipt.request_id > 0
                && receipt.request_id <= SAFE_INTEGER =>
            {
                Ok(event)
            }
            _ => Err(()),
        }
    }
}
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Command<'a> {
    protocol_version: u8,
    #[serde(rename = "type")]
    kind: &'static str,
    request: &'a IntentRequest,
}
pub(super) fn encode(request: &IntentRequest) -> Result<Vec<u8>, ()> {
    request.validate().map_err(|_| ())?;
    let mut bytes = serde_json::to_vec(&Command {
        protocol_version: 1,
        kind: "intent",
        request,
    })
    .map_err(|_| ())?;
    if bytes.len() > MAX_LINE {
        return Err(());
    }
    bytes.push(b'\n');
    Ok(bytes)
}

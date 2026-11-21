//! Local observation schema. Decoding is not registration or authority.
use super::model::{Intent, SAFE_INTEGER};
use serde::{Deserialize, Serialize};

macro_rules! values {
    ($name:ident { $($value:ident),+ }) => {
        #[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
        #[serde(rename_all = "kebab-case")]
        pub(crate) enum $name { $($value),+ }
    };
}
values!(Standby {
    Off,
    On,
    Stopping,
    Unknown
});
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum Registration {
    Disconnected,
    Connecting,
    Reconnecting,
    Authenticated,
    NeedsAction,
    Unknown,
}
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum RegistrationReason {
    IdentityRefused,
    ConnectionOccupied,
    GatewayRefused,
}
values!(Work {
    Idle,
    Preparing,
    Running,
    Stopping,
    Completed,
    Failed,
    Unknown
});
values!(Cleanup {
    Confirmed,
    Pending,
    Unknown
});
values!(TransitionOutcome {
    Pending,
    Applied,
    Rejected,
    Unknown
});
#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Transition {
    pub(super) request_id: u64,
    pub(super) intent: Intent,
    pub(super) outcome: TransitionOutcome,
}
#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Observation {
    pub(super) revision: u64,
    pub(super) valid_for_ms: u32,
    pub(super) standby: Standby,
    pub(super) registration: Registration,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub(super) registration_reason: Option<RegistrationReason>,
    pub(super) caller: Option<String>,
    pub(super) work: Work,
    pub(super) cleanup: Cleanup,
    pub(super) can_request_on: bool,
    pub(super) can_request_off: bool,
    pub(super) transition: Option<Transition>,
}
impl Observation {
    pub(super) fn unresolved(&self) -> Option<(u64, Intent)> {
        self.transition
            .as_ref()
            .filter(|t| {
                matches!(
                    t.outcome,
                    TransitionOutcome::Pending | TransitionOutcome::Unknown
                )
            })
            .map(|t| (t.request_id, t.intent))
    }

    pub(super) fn valid(&self) -> bool {
        self.revision > 0
            && self.revision <= SAFE_INTEGER
            && self.valid_for_ms <= 3000
            && self.caller.as_ref().is_none_or(|s| {
                !s.is_empty() && s.chars().count() <= 80 && !s.chars().any(char::is_control)
            })
            && !(self.can_request_on && self.can_request_off)
            && (self.valid_for_ms != 0 || (!self.can_request_on && !self.can_request_off))
            && (!self.can_request_on
                || (self.standby == Standby::Off
                    && self.work == Work::Idle
                    && self.cleanup == Cleanup::Confirmed))
            && (!self.can_request_off
                || (self.standby == Standby::On && self.cleanup != Cleanup::Unknown))
            && (self.cleanup != Cleanup::Unknown || (!self.can_request_on && !self.can_request_off))
            && (match self.registration {
                Registration::NeedsAction => {
                    self.registration_reason.is_some()
                        && self.standby == Standby::On
                        && self.work == Work::Idle
                        && self.cleanup == Cleanup::Confirmed
                        && !self.can_request_on
                        && self.can_request_off
                }
                _ => self.registration_reason.is_none(),
            })
            && self.transition.as_ref().is_none_or(|t| {
                t.request_id > 0
                    && t.request_id <= SAFE_INTEGER
                    && (t.outcome != TransitionOutcome::Unknown
                        || (!self.can_request_on && !self.can_request_off))
                    && (t.outcome != TransitionOutcome::Applied
                        || (self.cleanup != Cleanup::Unknown
                            && match t.intent {
                                Intent::On => self.standby == Standby::On,
                                Intent::Off => {
                                    self.standby == Standby::Off
                                        && self.work == Work::Idle
                                        && self.cleanup == Cleanup::Confirmed
                                }
                            }))
            })
    }
}

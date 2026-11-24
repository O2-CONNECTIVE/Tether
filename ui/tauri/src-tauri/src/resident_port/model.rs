use serde::{Deserialize, Serialize};
pub(super) const SAFE_INTEGER: u64 = 9_007_199_254_740_991;
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum Intent {
    On,
    Off,
    Extend,
}
fn deserialize_standby_hours<'de, D>(deserializer: D) -> Result<Option<u8>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    u8::deserialize(deserializer).map(Some)
}
#[derive(Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct IntentRequest {
    pub(super) protocol_version: u8,
    pub(super) request_id: u64,
    pub(super) expected_revision: u64,
    pub(super) intent: Intent,
    #[serde(
        default,
        deserialize_with = "deserialize_standby_hours",
        skip_serializing_if = "Option::is_none"
    )]
    pub(super) standby_hours: Option<u8>,
}
impl IntentRequest {
    pub(super) fn validate(&self) -> Result<(), &'static str> {
        if self.protocol_version != 1 {
            return Err("UNSUPPORTED_RESIDENT_VERSION");
        }
        if self.request_id == 0
            || self.request_id > SAFE_INTEGER
            || self.expected_revision > SAFE_INTEGER
        {
            return Err("INVALID_RESIDENT_INTENT");
        }
        let valid_hours = matches!(self.standby_hours, Some(2 | 4 | 8 | 12 | 24));
        match self.intent {
            Intent::On if self.standby_hours.is_some() && !valid_hours => {
                return Err("INVALID_RESIDENT_INTENT");
            }
            Intent::Off if self.standby_hours.is_some() => {
                return Err("INVALID_RESIDENT_INTENT");
            }
            Intent::Extend if !valid_hours => return Err("INVALID_RESIDENT_INTENT"),
            _ => {}
        }
        Ok(())
    }
}
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "kebab-case")]
pub(super) enum Disposition {
    Unavailable,
    Accepted,
    Rejected,
    Stale,
    Unknown,
}
#[derive(Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Receipt {
    pub(super) protocol_version: u8,
    pub(super) request_id: u64,
    pub(super) intent: Intent,
    pub(super) disposition: Disposition,
}
impl Receipt {
    pub(super) fn unavailable(request: IntentRequest) -> Self {
        Self::new(request, Disposition::Unavailable)
    }
    pub(super) fn new(request: IntentRequest, disposition: Disposition) -> Self {
        Self {
            protocol_version: 1,
            request_id: request.request_id,
            intent: request.intent,
            disposition,
        }
    }
}
#[derive(Clone, Serialize)]
#[serde(tag = "source", rename_all = "kebab-case")]
enum Source {
    Unsupported,
    ResidentOwner {
        #[serde(flatten)]
        output: Output,
    },
}
#[derive(Clone, Serialize)]
#[serde(tag = "state")]
enum Output {
    #[serde(rename = "source-unavailable")]
    Unavailable,
    #[cfg(all(target_os = "macos", resident_native_source))]
    #[serde(rename = "source-startup-checking")]
    StartupChecking,
    #[cfg(all(target_os = "macos", resident_native_source))]
    #[serde(rename = "source-policy-rejected")]
    PolicyRejected,
    // Fixture shape tests do not supply authentication or a native factory.
    #[cfg(any(test, target_os = "macos"))]
    #[serde(rename = "source-observation")]
    Observation {
        #[serde(flatten)]
        observation: super::observation::Observation,
    },
}
#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct Status {
    protocol_version: u8,
    #[serde(flatten)]
    source: Source,
}
impl Status {
    #[cfg(all(target_os = "macos", resident_native_source))]
    pub(super) fn startup_checking() -> Self {
        Self::owner(Output::StartupChecking)
    }
    #[cfg(all(target_os = "macos", resident_native_source))]
    pub(super) fn policy_rejected() -> Self {
        Self::owner(Output::PolicyRejected)
    }
    fn owner(output: Output) -> Self {
        Self {
            protocol_version: 1,
            source: Source::ResidentOwner { output },
        }
    }
    #[cfg(all(target_os = "macos", resident_native_source))]
    pub(super) fn cleanup_unknown(&self) -> bool {
        matches!(
            &self.source,
            Source::ResidentOwner {
                output: Output::Observation { observation },
            } if observation.cleanup == super::observation::Cleanup::Unknown
        )
    }
    pub(super) fn unbound() -> Self {
        if cfg!(target_os = "macos") {
            Self::owner(Output::Unavailable)
        } else {
            Self {
                protocol_version: 1,
                source: Source::Unsupported,
            }
        }
    }
    #[cfg(target_os = "macos")]
    pub(super) fn bound(source: &super::duplex::owner::BoundDuplex) -> Self {
        match source.current() {
            Some(observation) => Self::owner(Output::Observation { observation }),
            None => Self::unbound(),
        }
    }
}

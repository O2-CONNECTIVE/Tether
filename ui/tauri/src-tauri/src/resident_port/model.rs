use serde::{Deserialize, Serialize};
pub(super) const SAFE_INTEGER: u64 = 9_007_199_254_740_991;
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum Intent {
    On,
    Off,
}
#[derive(Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct IntentRequest {
    pub(super) protocol_version: u8,
    pub(super) request_id: u64,
    pub(super) expected_revision: u64,
    pub(super) intent: Intent,
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
#[serde(tag = "state", rename_all = "kebab-case")]
enum Output {
    SourceUnavailable,
    // Fixture shape tests do not supply authentication or a native factory.
    #[cfg(any(test, target_os = "macos"))]
    SourceObservation {
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
    pub(super) fn unbound() -> Self {
        Self {
            protocol_version: 1,
            source: if cfg!(target_os = "macos") {
                Source::ResidentOwner {
                    output: Output::SourceUnavailable,
                }
            } else {
                Source::Unsupported
            },
        }
    }
    #[cfg(target_os = "macos")]
    pub(super) fn bound(source: &super::duplex::owner::BoundDuplex) -> Self {
        match source.current() {
            Some(observation) => Self {
                protocol_version: 1,
                source: Source::ResidentOwner {
                    output: Output::SourceObservation { observation },
                },
            },
            None => Self::unbound(),
        }
    }
}

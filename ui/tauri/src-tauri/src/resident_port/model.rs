use serde::{Deserialize, Serialize};
pub(super) const SAFE_INTEGER: u64 = 9_007_199_254_740_991;
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "kebab-case")]
pub(crate) enum Intent {
    On,
    Off,
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

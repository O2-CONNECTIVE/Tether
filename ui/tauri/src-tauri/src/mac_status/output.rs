use serde::Serialize;

#[derive(Debug, Clone, Serialize)]
#[serde(tag = "state", rename_all = "kebab-case")]
pub(crate) enum MacOutput {
    SourceUnavailable,
    // Finite source fixtures are not an installable source or a live provider.
    #[cfg(test)]
    SyntheticObservation {
        view: OutputView,
        #[serde(rename = "validForMs")]
        valid_for_ms: u32,
        #[serde(rename = "cleanupUnknown")]
        cleanup_unknown: bool,
        #[serde(rename = "terminalHistory")]
        terminal_history: bool,
    },
}

#[cfg(test)]
#[derive(Debug, Clone, Serialize)]
pub(crate) struct OutputView {
    heading: &'static str,
    detail: &'static str,
    icon: &'static str,
}

#[cfg(test)]
impl MacOutput {
    // Only test builds can cross the validated-state output seam. No wire input,
    // reader, source clock, restart or mutable provider exists in production.
    pub(crate) fn synthetic(snapshot: &super::state::Snapshot, now: f64) -> Self {
        use super::presentation::{present, Icon};
        let view = present(snapshot, now);
        let freshness = if snapshot.fresh(now) {
            (snapshot.source_time + 3.0 - now).max(0.0)
        } else {
            0.0
        };
        let lifetime = snapshot.remaining_deadline.map_or(freshness, |deadline| {
            freshness.min((deadline - now).max(0.0))
        });
        let valid_for_ms = (lifetime * 1000.0).floor().clamp(0.0, 3000.0) as u32;
        Self::SyntheticObservation {
            view: OutputView {
                heading: view.heading,
                detail: view.detail,
                icon: match view.icon {
                    Icon::Checking => "checking",
                    Icon::Neutral => "neutral",
                    Icon::Warning => "warning",
                },
            },
            valid_for_ms,
            cleanup_unknown: snapshot.sticky_unknown
                || snapshot
                    .frame
                    .as_ref()
                    .is_some_and(|frame| frame.cleanup == "unknown"),
            terminal_history: snapshot.source_closed() && snapshot.terminal_observed(),
        }
    }
}

#[cfg(test)]
#[path = "output_tests.rs"]
mod tests;

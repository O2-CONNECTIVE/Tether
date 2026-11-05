use crate::mac_status::output::MacOutput;
use serde::Serialize;

#[derive(Debug, Clone, Serialize)]
#[serde(tag = "source", rename_all = "kebab-case")]
enum Source {
    WindowsShell,
    MacStatus {
        #[serde(flatten)]
        output: MacOutput,
    },
    Unsupported,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct StatusPresentation {
    protocol_version: u8,
    #[serde(flatten)]
    source: Source,
}

impl Default for StatusPresentation {
    fn default() -> Self {
        let source = if cfg!(target_os = "windows") {
            Source::WindowsShell
        } else if cfg!(target_os = "macos") {
            Source::MacStatus {
                output: MacOutput::SourceUnavailable,
            }
        } else {
            Source::Unsupported
        };
        Self {
            protocol_version: 1,
            source,
        }
    }
}

impl StatusPresentation {
    #[cfg(test)]
    pub(crate) fn synthetic(snapshot: &crate::mac_status::state::Snapshot, now: f64) -> Self {
        Self {
            protocol_version: 1,
            source: Source::MacStatus {
                output: MacOutput::synthetic(snapshot, now),
            },
        }
    }
}

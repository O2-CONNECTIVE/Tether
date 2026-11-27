use serde::{Deserialize, Serialize};
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc,
};

const STDOUT_LIMIT: usize = 4096;

#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum PairingResult {
    Succeeded,
    Cancelled,
    Rejected,
    ReRegistrationRequired,
    Blocked,
    Unknown,
    LocalApplyFailed,
    BrowserFailed,
    Busy,
    Unavailable,
}

#[derive(Clone, Debug, PartialEq, Eq)]
#[allow(
    dead_code,
    reason = "native helper output is only produced by signed-source builds"
)]
pub(crate) enum HelperOutput {
    Exited { status: i32, stdout: Vec<u8> },
    TimedOut,
    Unavailable,
}

#[cfg(any(test, all(target_os = "macos", resident_native_source)))]
fn native_helper_output(code: u32, length: u32, status: i32, mut stdout: Vec<u8>) -> HelperOutput {
    match code {
        1 if length as usize <= STDOUT_LIMIT && length as usize <= stdout.len() => {
            stdout.truncate(length as usize);
            HelperOutput::Exited { status, stdout }
        }
        2 => HelperOutput::TimedOut,
        _ => HelperOutput::Unavailable,
    }
}

pub(crate) trait HelperRunner: Send + Sync {
    fn run(&self) -> HelperOutput;
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct HelperResponse {
    protocol_version: u8,
    result: PairingResult,
}

pub(crate) fn decode_helper_output(output: HelperOutput) -> PairingResult {
    let HelperOutput::Exited { status: 0, stdout } = output else {
        return PairingResult::Unavailable;
    };
    if stdout.len() > STDOUT_LIMIT
        || stdout.first() != Some(&b'{')
        || !stdout.ends_with(b"}\n")
        || stdout[..stdout.len().saturating_sub(1)].contains(&b'\n')
        || stdout[..stdout.len().saturating_sub(1)].contains(&b'\r')
    {
        return PairingResult::Unavailable;
    }
    match serde_json::from_slice::<HelperResponse>(&stdout[..stdout.len() - 1]) {
        Ok(response) if response.protocol_version == 1 => response.result,
        _ => PairingResult::Unavailable,
    }
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct PairingResponse {
    result: PairingResult,
}

struct PairingPermit(Arc<AtomicBool>);
impl Drop for PairingPermit {
    fn drop(&mut self) {
        self.0.store(false, Ordering::Release);
    }
}

#[derive(Clone)]
pub(crate) struct PairingManager {
    busy: Arc<AtomicBool>,
    runner: Arc<dyn HelperRunner>,
}

impl PairingManager {
    pub(crate) fn new(runner: Arc<dyn HelperRunner>) -> Self {
        Self {
            busy: Arc::new(AtomicBool::new(false)),
            runner,
        }
    }

    pub(crate) fn production() -> Self {
        Self::new(production_runner())
    }

    fn reserve(&self) -> Option<PairingPermit> {
        self.busy
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
            .ok()
            .map(|_| PairingPermit(self.busy.clone()))
    }

    #[cfg(test)]
    fn run(&self) -> PairingResult {
        let Some(permit) = self.reserve() else {
            return PairingResult::Busy;
        };
        Self::run_reserved(self.runner.as_ref(), permit)
    }

    fn run_reserved(runner: &dyn HelperRunner, _permit: PairingPermit) -> PairingResult {
        decode_helper_output(runner.run())
    }

    async fn request(&self) -> PairingResponse {
        let Some(permit) = self.reserve() else {
            return PairingResponse {
                result: PairingResult::Busy,
            };
        };
        let runner = self.runner.clone();
        let result = tauri::async_runtime::spawn_blocking(move || {
            Self::run_reserved(runner.as_ref(), permit)
        })
        .await
        .unwrap_or(PairingResult::Unavailable);
        PairingResponse { result }
    }
}

#[tauri::command]
pub(crate) async fn request_resident_pairing(
    manager: tauri::State<'_, PairingManager>,
) -> Result<PairingResponse, &'static str> {
    let manager = manager.inner().clone();
    Ok(manager.request().await)
}

#[cfg(all(target_os = "macos", resident_native_source))]
struct NativeHelperRunner;
#[cfg(all(target_os = "macos", resident_native_source))]
unsafe extern "C" {
    fn rh_resident_host_pairing_run(
        stdout_bytes: *mut u8,
        capacity: u32,
        stdout_length: *mut u32,
        exit_status: *mut i32,
    ) -> u32;
}
#[cfg(all(target_os = "macos", resident_native_source))]
impl HelperRunner for NativeHelperRunner {
    fn run(&self) -> HelperOutput {
        let mut stdout = vec![0; STDOUT_LIMIT];
        let mut length = 0;
        let mut status = 0;
        // SAFETY: the Swift ABI writes at most the exact capacity and retains no pointers.
        let code = unsafe {
            rh_resident_host_pairing_run(stdout.as_mut_ptr(), 4096, &mut length, &mut status)
        };
        native_helper_output(code, length, status, stdout)
    }
}

#[cfg(not(all(target_os = "macos", resident_native_source)))]
struct UnavailableHelperRunner;
#[cfg(not(all(target_os = "macos", resident_native_source)))]
impl HelperRunner for UnavailableHelperRunner {
    fn run(&self) -> HelperOutput {
        HelperOutput::Unavailable
    }
}

#[cfg(all(target_os = "macos", resident_native_source))]
fn production_runner() -> Arc<dyn HelperRunner> {
    Arc::new(NativeHelperRunner)
}
#[cfg(not(all(target_os = "macos", resident_native_source)))]
fn production_runner() -> Arc<dyn HelperRunner> {
    Arc::new(UnavailableHelperRunner)
}

#[cfg(test)]
#[path = "resident_pairing_tests.rs"]
mod tests;

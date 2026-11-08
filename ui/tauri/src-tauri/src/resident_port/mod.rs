//! Concrete observation/intent boundary. Normal startup and IPC remain unbound.
pub(crate) mod duplex;
mod model;
mod observation;

use model::{IntentRequest, Receipt, Status};

mod port;
pub(crate) use port::ResidentPort;
#[cfg(all(target_os = "macos", resident_native_source))]
pub(crate) mod native_session;

#[derive(serde::Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct StatusRequest {
    protocol_version: u8,
}
#[tauri::command]
pub(crate) fn get_resident_status(
    request: StatusRequest,
    port: tauri::State<'_, ResidentPort>,
) -> Result<Status, &'static str> {
    if request.protocol_version != 1 {
        return Err("UNSUPPORTED_RESIDENT_VERSION");
    }
    Ok(port.current())
}
#[tauri::command]
pub(crate) async fn request_resident_intent(
    request: IntentRequest,
    port: tauri::State<'_, ResidentPort>,
) -> Result<Receipt, &'static str> {
    let fallback = request.clone();
    let response = port.request(request)?;
    // Wait only on the existing owned receipt channel, never on the UI thread.
    // A disconnected sender does not apply an intent or retry its submission.
    tauri::async_runtime::spawn_blocking(move || {
        response
            .recv()
            .unwrap_or_else(|_| Receipt::new(fallback, model::Disposition::Unknown))
    })
    .await
    .map_err(|_| "RESIDENT_SOURCE_UNAVAILABLE")
}

#[cfg(test)]
mod port_tests;

#[cfg(all(test, target_os = "macos"))]
mod port_pipe_tests;

#[cfg(test)]
mod ipc_tests;

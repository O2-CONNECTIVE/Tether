//! Concrete observation/intent boundary; native startup retains its whole owner.
pub(crate) mod duplex;
mod model;
mod observation;

use model::{IntentRequest, Receipt, Status};

mod port;
pub(crate) use port::ResidentPort;
mod state;
#[cfg(not(resident_gui_fixture))]
pub(crate) use state::ResidentLifetime;
pub(crate) use state::ResidentState;
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
    state: tauri::State<'_, std::sync::Arc<ResidentState>>,
) -> Result<Status, &'static str> {
    if request.protocol_version != 1 {
        return Err("UNSUPPORTED_RESIDENT_VERSION");
    }
    Ok(state.port().current())
}
#[tauri::command]
pub(crate) async fn request_resident_intent(
    request: IntentRequest,
    state: tauri::State<'_, std::sync::Arc<ResidentState>>,
) -> Result<Receipt, &'static str> {
    let fallback = request.clone();
    let response = state.port().request(request)?;
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

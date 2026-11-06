//! Concrete observation/intent boundary. Normal startup and IPC remain unbound.
pub(crate) mod duplex;
mod model;
mod observation;

use model::{IntentRequest, Receipt, Status};
use std::sync::mpsc::{self, Receiver};

#[derive(Default)]
pub(crate) struct ResidentPort {
    #[cfg(target_os = "macos")]
    bound: Option<duplex::owner::BoundDuplex>,
}
impl ResidentPort {
    // Concrete and private: future sealed launcher must establish provenance
    // before using this path. No normal caller, arbitrary factory or rebind.
    #[cfg(target_os = "macos")]
    fn bound(source: duplex::owner::BoundDuplex) -> std::io::Result<Self> {
        source.first()?; // off UI thread, before publishing this same source
        Ok(Self {
            bound: Some(source),
        })
    }
    pub(crate) fn current(&self) -> Status {
        #[cfg(target_os = "macos")]
        if let Some(source) = &self.bound {
            return Status::bound(source);
        }
        Status::unbound()
    }
    // Receipt is acceptance only, not a replacement observation. The future
    // async IPC caller must await this receiver off the UI thread.
    pub(crate) fn request(
        &self,
        request: IntentRequest,
    ) -> Result<Receiver<Receipt>, &'static str> {
        request.validate()?;
        #[cfg(target_os = "macos")]
        if let Some(source) = &self.bound {
            return source
                .request(request)
                .map_err(|_| "RESIDENT_SOURCE_UNAVAILABLE");
        }
        let (send, receive) = mpsc::sync_channel(1);
        let _ = send.try_send(Receipt::unavailable(request));
        Ok(receive)
    }
}

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

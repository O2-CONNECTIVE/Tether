//! Typed source/intent core; binding does not establish producer authority.
#[cfg(target_os = "macos")]
use super::duplex;
use super::model::{IntentRequest, Receipt, Status};
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
    pub(crate) fn bound(source: duplex::owner::BoundDuplex) -> std::io::Result<Self> {
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

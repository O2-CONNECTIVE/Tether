//! Typed source/intent core; binding does not establish producer authority.
#[cfg(target_os = "macos")]
use super::duplex;
#[cfg(all(target_os = "macos", resident_gui_fixture))]
use super::model::{Disposition, Intent};
use super::model::{IntentRequest, Receipt, Status};
#[cfg(all(target_os = "macos", resident_gui_fixture))]
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{self, Receiver};

#[derive(Default)]
pub(crate) struct ResidentPort {
    #[cfg(target_os = "macos")]
    bound: Option<duplex::owner::BoundDuplex>,
    #[cfg(all(target_os = "macos", resident_gui_fixture))]
    menu_stop_receipt_id: AtomicU64,
}
impl ResidentPort {
    // Concrete and private: future sealed launcher must establish provenance
    // before using this path. No normal caller, arbitrary factory or rebind.
    #[cfg(target_os = "macos")]
    pub(crate) fn bound(source: duplex::owner::BoundDuplex) -> std::io::Result<Self> {
        source.first()?; // off UI thread, before publishing this same source
        Ok(Self {
            bound: Some(source),
            #[cfg(resident_gui_fixture)]
            menu_stop_receipt_id: AtomicU64::new(0),
        })
    }
    #[cfg(all(target_os = "macos", resident_gui_fixture))]
    pub(crate) fn expect_menu_stop_receipt(&self, id: u64) {
        self.menu_stop_receipt_id.store(id, Ordering::SeqCst);
    }
    #[cfg(all(target_os = "macos", resident_gui_fixture))]
    pub(super) fn take_menu_stop_receipt_label(&self, receipt: &Receipt) -> Option<&'static str> {
        if receipt.intent != Intent::Off
            || receipt.request_id == 0
            || self
                .menu_stop_receipt_id
                .compare_exchange(receipt.request_id, 0, Ordering::SeqCst, Ordering::SeqCst)
                .is_err()
        {
            return None;
        }
        Some(match receipt.disposition {
            Disposition::Accepted => "menu-stop-receipt-accepted",
            Disposition::Rejected => "menu-stop-receipt-rejected",
            Disposition::Stale => "menu-stop-receipt-stale",
            Disposition::Unavailable => "menu-stop-receipt-unavailable",
            Disposition::Unknown => "menu-stop-receipt-unknown",
        })
    }
    pub(crate) fn current(&self) -> Status {
        #[cfg(target_os = "macos")]
        if let Some(source) = &self.bound {
            return Status::bound(source);
        }
        Status::unbound()
    }
    #[cfg(target_os = "macos")]
    pub(crate) fn has_source(&self) -> bool {
        self.bound.is_some()
    }
    #[cfg(target_os = "macos")]
    pub(crate) fn standby_deadline(&self) -> Option<super::observation::StandbyDeadline> {
        self.bound.as_ref()?.standby_deadline()
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

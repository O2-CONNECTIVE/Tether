//! Watches the original resident port independently of the WebView.
use crate::resident_port::{ResidentState, StandbyDeadline};
use std::sync::{
    atomic::{AtomicBool, Ordering},
    mpsc, Arc,
};
use std::thread::{self, JoinHandle};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

enum Wake {
    Stop,
    PermissionChecked,
    WarningChecked { request_id: u64, superseded: bool },
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Permission {
    NotRequested,
    Checking,
    Checked,
}

pub(super) struct Watcher {
    wake: mpsc::Sender<Wake>,
    running: Arc<AtomicBool>,
    thread: Option<JoinHandle<()>>,
}

impl Watcher {
    pub(super) fn start(owner: Arc<ResidentState>) -> Option<Self> {
        if !owner.port().has_source() {
            return None;
        }
        let (wake, receiver) = mpsc::channel();
        let sender = wake.clone();
        let running = Arc::new(AtomicBool::new(true));
        let active = running.clone();
        let thread = thread::Builder::new()
            .name("standby-deadline-notice".into())
            .spawn(move || watch(&owner, &sender, receiver, &active))
            .ok()?;
        Some(Self {
            wake,
            running,
            thread: Some(thread),
        })
    }
}

impl Drop for Watcher {
    fn drop(&mut self) {
        self.running.store(false, Ordering::Release);
        let _ = self.wake.send(Wake::Stop);
        // The observer never waits for Cocoa or a permission callback.
        // Its thread must finish before the app releases its resident state.
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

fn watch(
    owner: &Arc<ResidentState>,
    wake: &mpsc::Sender<Wake>,
    receiver: mpsc::Receiver<Wake>,
    running: &Arc<AtomicBool>,
) {
    let mut permission = Permission::NotRequested;
    let mut warning = Warning::default();
    while running.load(Ordering::Acquire) {
        if let (Some(deadline), Some(now)) = (owner.port().standby_deadline(), wall_ms()) {
            if permission == Permission::NotRequested && u128::from(deadline.unix_ms) > now {
                permission = Permission::Checking;
                let wake = wake.clone();
                super::mac_notifications::request_permission(move |_| {
                    let _ = wake.send(Wake::PermissionChecked);
                });
            }
            if permission == Permission::Checked && warning.reserve(deadline, now) {
                let owner = Arc::downgrade(owner);
                let running = running.clone();
                let wake = wake.clone();
                let request_id = deadline.request_id;
                let superseded = Arc::new(AtomicBool::new(false));
                let checked = superseded.clone();
                super::mac_notifications::post_if_authorized(
                    move || {
                        let current = running.load(Ordering::Acquire)
                            && owner.upgrade().is_some_and(|owner| {
                                still_current(
                                    request_id,
                                    owner.port().standby_deadline(),
                                    wall_ms(),
                                )
                            });
                        checked.store(!current, Ordering::Release);
                        current
                    },
                    move |_| {
                        let _ = wake.send(Wake::WarningChecked {
                            request_id,
                            superseded: superseded.load(Ordering::Acquire),
                        });
                    },
                );
            }
        }
        match receiver.recv_timeout(Duration::from_millis(500)) {
            Ok(Wake::PermissionChecked) => permission = Permission::Checked,
            Ok(Wake::WarningChecked {
                request_id,
                superseded,
            }) => {
                warning.finish(request_id, superseded);
            }
            Ok(Wake::Stop) | Err(mpsc::RecvTimeoutError::Disconnected) => return,
            Err(mpsc::RecvTimeoutError::Timeout) => {}
        }
    }
}

fn wall_ms() -> Option<u128> {
    Some(
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .ok()?
            .as_millis(),
    )
}

#[derive(Default)]
struct Warning {
    pending_request: Option<u64>,
    attempted_request: u64,
}

impl Warning {
    fn reserve(&mut self, deadline: StandbyDeadline, now: u128) -> bool {
        if self.pending_request.is_some()
            || deadline.request_id <= self.attempted_request
            || !due(deadline, now)
        {
            return false;
        }
        self.pending_request = Some(deadline.request_id);
        true
    }

    fn finish(&mut self, request_id: u64, superseded: bool) {
        if self.pending_request == Some(request_id) {
            self.pending_request = None;
            // A final owner recheck can veto before submitting to macOS. Only
            // this known no-submit case can retry the same deadline.
            if !superseded {
                self.attempted_request = request_id;
            }
        }
    }
}

fn due(deadline: StandbyDeadline, now: u128) -> bool {
    deadline.ending_soon && u128::from(deadline.unix_ms) > now
}

fn still_current(request_id: u64, deadline: Option<StandbyDeadline>, now: Option<u128>) -> bool {
    match (deadline, now) {
        (Some(deadline), Some(now)) => deadline.request_id == request_id && due(deadline, now),
        _ => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn deadline(request_id: u64, unix_ms: u64, ending_soon: bool) -> StandbyDeadline {
        StandbyDeadline {
            request_id,
            unix_ms,
            ending_soon,
        }
    }

    #[test]
    fn one_attempt_for_one_owner_deadline_despite_clock_changes() {
        let mut warning = Warning::default();
        assert!(!warning.reserve(deadline(1, 100, false), 50));
        assert!(warning.reserve(deadline(1, 100, true), 50));
        warning.finish(1, false);
        assert!(!warning.reserve(deadline(1, 90, true), 50));
        assert!(!warning.reserve(deadline(1, 90, false), 50));
        assert!(!warning.reserve(deadline(1, 90, true), 50));
        assert!(!warning.reserve(deadline(2, 100, true), 100));
        assert!(!warning.reserve(deadline(2, 200, false), 150));
        assert!(warning.reserve(deadline(2, 200, true), 150));
    }

    #[test]
    fn only_a_pre_submission_veto_allows_retry_for_the_same_deadline() {
        let mut warning = Warning::default();
        let sample = deadline(1, 100, true);
        assert!(warning.reserve(sample, 50));
        assert!(!warning.reserve(sample, 50));
        warning.finish(1, true);
        assert!(warning.reserve(sample, 50));
        warning.finish(1, false);
        assert!(!warning.reserve(sample, 50));
    }

    #[test]
    fn old_or_absent_observation_cancels_a_queued_notice() {
        assert!(still_current(1, Some(deadline(1, 110, true)), Some(100)));
        assert!(!still_current(1, Some(deadline(2, 110, true)), Some(100)));
        assert!(!still_current(1, Some(deadline(1, 110, false)), Some(100)));
        assert!(!still_current(1, Some(deadline(1, 110, true)), Some(110)));
        assert!(!still_current(1, None, Some(100)));
        assert!(!still_current(1, Some(deadline(1, 110, true)), None));
    }
}

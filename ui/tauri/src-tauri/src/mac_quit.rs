//! Confirm a person’s Quit before requesting the original owner’s Close.
use crate::resident_port::{ExitProgress, ResidentState};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager};

// Give the producer's 75s Close budget time to report evidence and join.
// This only bounds how long the UI waits for an outcome.
const WAIT_FOR_PROOF: Duration = Duration::from_secs(80);

#[derive(Clone, Copy, Default, PartialEq, Eq)]
enum Phase {
    #[default]
    Idle,
    Confirming,
    Closing,
    Ready,
    NeedsCleanup,
}

#[derive(Default)]
pub(super) struct Quit(Mutex<Phase>);

impl Quit {
    fn update(&self, from: Phase, to: Phase) -> bool {
        let mut phase = self.0.lock().unwrap_or_else(|e| e.into_inner());
        if *phase != from {
            return false;
        }
        *phase = to;
        true
    }

    #[cfg(any(not(resident_gui_fixture), test))]
    fn ready(&self) -> bool {
        *self.0.lock().unwrap_or_else(|e| e.into_inner()) == Phase::Ready
    }

    fn proceed(&self, confirmed: bool) -> bool {
        let next = if confirmed {
            Phase::Closing
        } else {
            Phase::Idle
        };
        self.update(Phase::Confirming, next) && confirmed
    }
}

#[cfg(not(resident_gui_fixture))]
pub(super) fn exit_requested(app: &AppHandle, api: &tauri::ExitRequestApi) {
    if app.state::<Quit>().ready() && app.state::<Arc<ResidentState>>().finish_requested() {
        return;
    }
    api.prevent_exit();
    request(app);
}

pub(super) fn request(app: &AppHandle) {
    let Some(quit) = app.try_state::<Quit>() else {
        return;
    };
    if !quit.update(Phase::Idle, Phase::Confirming) {
        return;
    }
    let handle = app.clone();
    if app.run_on_main_thread(move || confirm(handle)).is_err() {
        quit.update(Phase::Confirming, Phase::Idle);
    }
}

fn confirm(app: AppHandle) {
    let quit = app.state::<Quit>();
    if !quit.proceed(super::mac_quit_dialog::confirm()) {
        return;
    }
    let owner = app.state::<Arc<ResidentState>>().inner().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let confirmed = owner.request_exit()
            || wait_for_proof(|| owner.exit_progress(), Instant::now() + WAIT_FOR_PROOF);
        let handle = app.clone();
        let _ = app.run_on_main_thread(move || finish(&handle, confirmed));
    });
}

fn wait_for_proof(mut poll: impl FnMut() -> ExitProgress, deadline: Instant) -> bool {
    loop {
        match poll() {
            ExitProgress::Confirmed => return true,
            ExitProgress::Unconfirmed => return false,
            ExitProgress::Waiting => {}
        }
        let Some(remaining) = deadline.checked_duration_since(Instant::now()) else {
            return false;
        };
        std::thread::sleep(remaining.min(Duration::from_millis(100)));
    }
}

fn finish(app: &AppHandle, confirmed: bool) {
    let quit = app.state::<Quit>();
    if confirmed {
        if quit.update(Phase::Closing, Phase::Ready) {
            // AppHandle::exit has a direct-exit fallback if dispatch fails.
            // This call is reachable only after the same owner proved its join.
            app.exit(0);
        }
    } else if quit.update(Phase::Closing, Phase::NeedsCleanup) {
        super::mac_quit_dialog::show_cleanup();
        quit.update(Phase::NeedsCleanup, Phase::Idle);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::resident_port::duplex::owner::Supervisor;
    use crate::standby_lifecycle::StandbyOwner;

    #[test]
    fn cancellation_and_a_second_request_cannot_unlock_exit() {
        let quit = Quit::default();
        assert!(quit.update(Phase::Idle, Phase::Confirming));
        assert!(!quit.update(Phase::Idle, Phase::Confirming));
        assert!(!quit.proceed(false));
        assert!(!quit.update(Phase::Closing, Phase::Ready));
        assert!(!quit.ready());
        assert!(quit.update(Phase::Idle, Phase::Confirming));
        assert!(quit.proceed(true));
        assert!(!quit.update(Phase::Idle, Phase::Confirming));
        assert!(!quit.ready());
    }

    #[test]
    fn cancel_keeps_the_owner_and_confirm_waits_for_its_real_join() {
        let (producer, port) = Supervisor::retained_close_fixture();
        let mut owner = StandbyOwner::start(producer).unwrap();
        let gate = owner.gate.clone();
        let quit = Quit::default();
        let fresh = port.first_with_timeout(Duration::from_secs(2)).is_ok();
        let began = quit.update(Phase::Idle, Phase::Confirming);
        let cancelled = !quit.proceed(false);
        let still_owned =
            gate.outcome().is_none() && port.first_with_timeout(Duration::from_secs(2)).is_ok();
        let confirmed = quit.update(Phase::Idle, Phase::Confirming) && quit.proceed(true);
        let requested = gate.request_exit();
        let completed = requested
            || wait_for_proof(
                || {
                    if gate.finish_requested() {
                        ExitProgress::Confirmed
                    } else {
                        ExitProgress::Waiting
                    }
                },
                Instant::now() + Duration::from_secs(5),
            );
        owner.join();
        assert!(fresh && began && cancelled && still_owned && confirmed && completed);
        assert!(quit.update(Phase::Closing, Phase::Ready) && quit.ready());
    }

    #[test]
    fn only_confirmed_join_completes_the_wait() {
        let mut proof = [ExitProgress::Waiting, ExitProgress::Confirmed].into_iter();
        assert!(wait_for_proof(
            || proof.next().expect("only two polls"),
            Instant::now() + Duration::from_secs(10),
        ));
        assert!(!wait_for_proof(
            || ExitProgress::Unconfirmed,
            Instant::now() + Duration::from_secs(10),
        ));
        assert!(!wait_for_proof(|| ExitProgress::Waiting, Instant::now()));
    }
}

//! Real GUI managed state and original native self-child, never product trust.
#[cfg(resident_launch_fixture)]
use super::state::ResidentLifetime;
use super::state::ResidentState;
#[cfg(resident_launch_fixture)]
use crate::native_preparation::{FixedActiveFixture, NativePreparation, Preparation};
use serde_json::{Value, json};
use std::time::Duration;
#[cfg(resident_launch_fixture)]
use std::time::Instant;
fn status(state: &ResidentState) -> Value {
    serde_json::to_value(state.port().current()).unwrap()
}
fn receipt(state: &ResidentState, id: u64) -> Value {
    serde_json::to_value(
        state
            .port()
            .request(
                serde_json::from_value(json!({
                    "protocolVersion":1,"requestId":id,"expectedRevision":1,"intent":"on"
                }))
                .unwrap(),
            )
            .unwrap()
            .recv_timeout(Duration::from_secs(3))
            .unwrap(),
    )
    .unwrap()
}
fn require_managed<T: Send + Sync + 'static>() {}
pub(crate) fn run(mode: &str) {
    require_managed::<ResidentState>();
    #[cfg(resident_launch_fixture)]
    if matches!(mode, "gui-state-return" | "gui-state-unwind") {
        guarded(mode);
        return;
    }
    let mut passed = 0;
    let mut check = |condition: bool| {
        assert!(condition, "gui state condition {}", passed + 1);
        passed += 1;
    };
    if mode == "gui-state-normal" {
        let state = ResidentState::default();
        check(status(&state)["state"] == "source-unavailable");
        check(receipt(&state, 1)["disposition"] == "unavailable");
        check(state.request_exit());
        check(state.finish_requested());
        drop(state);
        // Explicit zero-input native variant is distinct from default startup.
        // Refused cannot become an unbound state's permission to exit.
        let state = ResidentState::prepare_native().unwrap();
        check(status(&state)["state"] == "source-unavailable");
        check(!state.request_exit());
        std::thread::sleep(Duration::from_millis(20));
        check(!state.finish_requested());
        check(!state.request_exit());
        drop(state);
        report(mode, passed, false, false);
        return;
    }
    #[cfg(resident_launch_fixture)]
    {
        assert!(matches!(mode, "gui-state-confirmed" | "gui-state-unknown"));
        let terminal = mode == "gui-state-confirmed";
        let native = ResidentState::fixture(FixedActiveFixture::Echo).unwrap();
        // Confirmed exercises the actual outer guard. Unknown is observed as
        // refused and retained by the native slot; dropping a lifetime guard
        // for Unknown intentionally never returns and is not claimed tested.
        let (lifetime, state) = if terminal {
            let lifetime = ResidentLifetime::new(native);
            let state = lifetime.state();
            (Some(lifetime), state)
        } else {
            (None, std::sync::Arc::new(native))
        };
        check(status(&state)["standby"] == "off");
        check(!state.finish_requested());
        check(receipt(&state, if terminal { 1 } else { 2 })["disposition"] == "accepted");
        check(status(&state)["standby"] == "off");
        check(NativePreparation::fixture_active_stats().waits == 0);
        let immediate = state.request_exit();
        check(!immediate || (terminal && state.finish_requested()));
        check(receipt(&state, 3)["disposition"] == "rejected");
        check(status(&state)["validForMs"] == 0);
        let until = Instant::now() + Duration::from_secs(3);
        let joined = loop {
            if let Some(joined) = state.fixture_poll_join() {
                break joined;
            }
            assert!(Instant::now() < until, "original outer join unavailable");
            std::thread::sleep(Duration::from_millis(1));
        };
        check(joined.native().terminal_confirmed() == terminal);
        check(joined.wire_close_confirmed() == terminal);
        check(state.finish_requested() == terminal);
        check(state.request_exit() == terminal);
        check(state.request_exit() == terminal); // idempotent close, no new owner
        check(receipt(&state, 4)["disposition"] == "rejected");
        drop(lifetime); // Confirmed outer lifetime requires same-state finish.
        drop(state); // same session guard, even Unknown must join original owner
        check(NativePreparation::fixture_active_stats().waits == 1);
        check(NativePreparation::fixture_counts().open == 0);
        check(NativePreparation::fixture_counts().closes == 4);
        check(NativePreparation::fixture_retained_state() == if terminal { 7 } else { 3 });
        check(matches!(
            NativePreparation::fixture_active(FixedActiveFixture::Echo),
            Preparation::Refused
        ));
        check(NativePreparation::fixture_active_stats().waits == 1);
        report(mode, passed, true, !terminal);
    }
    #[cfg(not(resident_launch_fixture))]
    panic!("active GUI state fixture unavailable");
}
fn report(mode: &str, passed: usize, child: bool, unknown: bool) {
    let child = u8::from(child);
    let unknown = u8::from(unknown);
    println!(
        "GUI_STATE_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted={child} childrenWaited={child} open=0 nativeUnknown={unknown} producer=0"
    );
}

#[cfg(resident_launch_fixture)]
fn guarded(mode: &str) {
    let mut passed = 0;
    let mut check = |condition: bool| {
        assert!(condition, "gui guard condition {}", passed + 1);
        passed += 1;
    };
    let lifetime = ResidentLifetime::new(ResidentState::fixture(FixedActiveFixture::Echo).unwrap());
    // Keep a real view alive across guard Drop. NativeSession's own Drop cannot
    // rescue a missing guard Close/join: the same state remains externally held.
    let view = lifetime.state();
    check(status(&view)["standby"] == "off");
    check(NativePreparation::fixture_active_stats().waits == 0);
    check(!view.finish_requested());
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(move || {
        let _lifetime = lifetime;
        if mode == "gui-state-unwind" {
            panic!("anonymous setup unwind");
        }
    }));
    check(result.is_err() == (mode == "gui-state-unwind"));
    let joined = view
        .fixture_poll_join()
        .expect("original joined proof missing");
    check(joined.native().terminal_confirmed());
    check(joined.wire_close_confirmed());
    check(view.finish_requested());
    check(NativePreparation::fixture_active_stats().waits == 1);
    check(NativePreparation::fixture_counts().open == 0);
    check(NativePreparation::fixture_counts().closes == 4);
    check(receipt(&view, 1)["disposition"] == "rejected");
    check(NativePreparation::fixture_retained_state() == 7);
    drop(view);
    check(matches!(
        NativePreparation::fixture_active(FixedActiveFixture::Echo),
        Preparation::Refused
    ));
    check(NativePreparation::fixture_active_stats().waits == 1);
    report(mode, passed, true, false);
}

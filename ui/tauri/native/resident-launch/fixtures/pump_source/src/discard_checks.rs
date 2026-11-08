//! Same-owner shutdown ABI negatives and trailing bytes after exact Wait.
use crate::native_preparation::{ExitObservation, FixedActiveFixture, NativePreparation, State};

#[cfg(resident_launch_fixture)]
pub(super) fn run(mode: &str) {
    use crate::native_preparation::{DiscardObservation as D, Preparation};
    use std::io::{Read, Write};
    let pending = mode == "supervisor-discard-pending";
    let checks = std::thread::spawn(move || {
        let preparation = NativePreparation::fixture_active(if pending {
            FixedActiveFixture::FiniteHeldUnknown
        } else {
            FixedActiveFixture::Early
        });
        let mut original = match preparation {
            Preparation::Owned(owner) | Preparation::Unknown(owner) => owner,
            Preparation::Refused => panic!("fixed owner"),
        };
        let mut checks = vec![
            original.fixture_discard_rejected(),
            original.fixture_foreign_active_calls(),
        ];
        // Wait first, deliberately leaving all finite output unread in the pipe.
        if !pending {
            loop {
                if original.poll_exit() == ExitObservation::Exit(0) {
                    break;
                }
                std::thread::sleep(std::time::Duration::from_millis(1));
            }
        }
        original.request_active_close();
        original.request_active_close(); // No integer close retry, no early retire.
        checks.push(original.state() == State::Unknown);
        checks.push(original.fixture_foreign_active_calls());
        checks.push(original.fixture_unknown_bytes_rejected());
        checks.push(original.io().read(&mut [0]).is_err());
        checks.push(original.io().write(b"x").is_err());
        if pending {
            // No child stdout can exist before its held control EOF is released.
            let empty_open = original.discard_observation() == D::Pending;
            let released = original.fixture_release_control_gate();
            let released_once = !original.fixture_release_control_gate();
            let mut progress = 0;
            let mut saw_eof = false;
            // Fixture failure escape only; after this bound still collect the
            // same self-timed child and retire before asserting anything.
            let read_until = std::time::Instant::now() + std::time::Duration::from_secs(10);
            while std::time::Instant::now() < read_until {
                match original.discard_observation() {
                    D::Pending => {
                        std::thread::yield_now();
                    }
                    D::Progress => progress += 1,
                    D::Eof => {
                        saw_eof = true;
                        break;
                    }
                    D::Error | D::Rejected => break,
                }
                let _ = original.poll_exit();
            }
            // One call discards at most4096, and Pending never masquerades as EOF.
            checks.push(empty_open && released && released_once);
            checks.push(saw_eof && progress >= 256);
            while matches!(original.poll_exit(), ExitObservation::Pending) {
                std::thread::yield_now();
            }
        } else {
            checks.push(non_pending(&mut original) == D::Progress);
            checks.push(non_pending(&mut original) == D::Eof);
        }
        let exit = original.poll_exit();
        if let ExitObservation::Exit(code) = exit {
            eprintln!("NATIVE_SUPERVISOR_CHILD_EXIT {code}");
        }
        checks.push(exit == ExitObservation::Exit(0));
        // Same completion predicate as the supervisor, after actual Wait.
        checks.extend([
            !D::Pending.is_eof(),
            !D::Rejected.is_eof(),
            !D::Error.is_eof(),
            D::Eof.is_eof(),
        ]);
        checks.push(original.retire_active_io() == State::Unknown);
        checks.push(original.discard_observation() == D::Rejected);
        checks.push(original.fixture_discard_rejected());
        checks.push(NativePreparation::fixture_counts().closes == 4);
        checks.push(NativePreparation::fixture_counts().open == 0);
        checks.push(NativePreparation::fixture_active_stats().waits == 1);
        checks.push(matches!(
            NativePreparation::fixture_active(FixedActiveFixture::Early),
            Preparation::Refused
        ));
        drop(original);
        checks
    })
    .join()
    .unwrap(); // Assert only after original Wait, IO cleanup, actual join.
    let passed = checks.len() + 2;
    for (index, value) in checks.iter().enumerate() {
        assert!(*value, "native supervisor condition {}", index + 1);
    }
    assert!(
        NativePreparation::fixture_retained_state() == 3,
        "native supervisor condition {}",
        passed - 1
    );
    assert!(
        NativePreparation::fixture_collect_retained_active() == ExitObservation::Unavailable,
        "native supervisor condition {passed}"
    );
    println!(
        "NATIVE_SUPERVISOR_FIXTURE mode={mode} passed={passed} failed=0 childrenStarted=1 childrenWaited=1 open=0 unknown=1 producer=0"
    );
}

// EINTR legitimately maps to Pending even after exact Wait. Retry only Pending
// with a fixed fixture budget, then collect/retire before checking the result.
fn non_pending(original: &mut NativePreparation) -> crate::native_preparation::DiscardObservation {
    use crate::native_preparation::DiscardObservation as D;
    for _ in 0..1024 {
        let value = original.discard_observation();
        if value != D::Pending {
            return value;
        }
        std::thread::yield_now();
    }
    D::Pending // Budget exhaustion is failure, never invented progress/EOF.
}

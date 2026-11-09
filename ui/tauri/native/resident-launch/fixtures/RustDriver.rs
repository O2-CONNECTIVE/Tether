use resident_preparation::{NativePreparation, Preparation};

struct Checks(u32);
impl Checks {
    fn check(&mut self, condition: bool) {
        assert!(
            condition,
            "native Rust owner condition {} failed",
            self.0 + 1
        );
        self.0 += 1;
    }
}

#[cfg(resident_launch_fixture)]
fn owned(mode: resident_preparation::FixedFixture) -> NativePreparation {
    match NativePreparation::fixture(mode) {
        Preparation::Owned(owner) => owner,
        _ => panic!("native fixture did not return the owned unlaunched Process"),
    }
}

#[cfg(resident_launch_fixture)]
fn fixture(mode: &str, checks: &mut Checks) {
    use resident_preparation::{Counts, FixedFixture as F, State as S};
    match mode {
        "launch-refusal" => {
            let mut owner = owned(F::Normal);
            checks.check(owner.launch() == S::OwnedUnlaunched);
            checks.check(owner.state() == S::OwnedUnlaunched);
            checks.check(owner.fixture_probe());
            checks.check(owner.poll_exit() == resident_preparation::ExitObservation::Unavailable);
            checks.check(owner.launch() == S::OwnedUnlaunched);
            checks.check(owner.retire() == S::RetiredUnlaunched);
            checks.check(owner.launch() == S::RetiredUnlaunched);
            checks.check(NativePreparation::fixture_counts().closes == 4);
        }
        "lifetime" => {
            let mut owner = owned(F::Normal);
            checks.check(owner.state() == S::OwnedUnlaunched);
            checks.check(owner.fixture_probe());
            checks.check(matches!(
                NativePreparation::fixture(F::Normal),
                Preparation::Refused
            ));
            checks.check(owner.retire() == S::RetiredUnlaunched);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
            checks.check(owner.retire() == S::RetiredUnlaunched);
            checks.check(owner.fixture_native_state() == 0);
            drop(owner);
            checks.check(NativePreparation::fixture_counts().retire_calls == 1);
            let next = owned(F::Normal);
            checks.check(next.fixture_probe());
            drop(next);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 8,
                        retire_calls: 2,
                    },
            );
        }
        "drop" => {
            {
                let owner = owned(F::Normal);
                checks.check(owner.fixture_probe());
            }
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
            let next = owned(F::Normal);
            checks.check(next.fixture_probe());
            drop(next);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 8,
                        retire_calls: 2,
                    },
            );
        }
        "io" => {
            let mut owner = owned(F::Normal);
            checks.check(owner.fixture_exchange());
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 3,
                        closes: 1,
                        retire_calls: 0,
                    },
            );
            checks.check(owner.state() == S::OwnedUnlaunched);
            checks.check(owner.retire() == S::RetiredUnlaunched);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
        }
        "unknown" => {
            let mut owner = owned(F::LostCloseReceipt);
            checks.check(owner.state() == S::OwnedUnlaunched);
            checks.check(owner.retire() == S::Unknown);
            checks.check(owner.state() == S::Unknown);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
            checks.check(owner.retire() == S::Unknown);
            drop(owner);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
            checks.check(matches!(
                NativePreparation::fixture(F::Normal),
                Preparation::Refused
            ));
        }
        "acquire-unknown" => {
            let Preparation::Unknown(mut owner) = NativePreparation::fixture(F::PartialLostReceipt)
            else {
                panic!("partial unknown was refused or misrepresented as owned");
            };
            checks.check(owner.state() == S::Unknown);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 2,
                        retire_calls: 0,
                    },
            );
            checks.check(owner.retire() == S::Unknown);
            drop(owner);
            checks.check(NativePreparation::fixture_counts().retire_calls == 0);
            checks.check(matches!(
                NativePreparation::fixture(F::Normal),
                Preparation::Refused
            ));
        }
        "thread-exit" => {
            let worker = std::thread::Builder::new()
                .spawn(|| {
                    let owner = owned(F::Normal);
                    let intact = owner.fixture_probe();
                    std::mem::forget(owner); // Native TLS still owns the original Process.
                    intact
                })
                .expect("own fixture worker creation failed");
            checks.check(worker.join().expect("own fixture worker panicked"));
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 0,
                    },
            );
            checks.check(matches!(
                NativePreparation::fixture(F::Normal),
                Preparation::Refused
            ));
        }
        "unwind" => {
            let hook = std::panic::take_hook();
            std::panic::set_hook(Box::new(|_| {}));
            let result = std::panic::catch_unwind(|| {
                let owner = owned(F::Normal);
                if !owner.fixture_probe() {
                    return false;
                }
                panic!("self-owned fixture unwind");
            });
            std::panic::set_hook(hook);
            checks.check(result.is_err());
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
            let next = owned(F::Normal);
            checks.check(next.fixture_probe());
            drop(next);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 8,
                        retire_calls: 2,
                    },
            );
        }
        "byte-stream" => {
            use std::io::{ErrorKind, Read, Write};
            let mut owner = owned(F::Normal);
            checks.check(owner.fixture_probe());
            {
                let mut view = owner.io();
                checks.check(view.read(&mut []).unwrap() == 0);
                checks.check(view.write(&[]).unwrap() == 0);
                checks.check(view.flush().is_ok());
                checks.check(view.read(&mut [0]).unwrap_err().kind() == ErrorKind::WouldBlock);
            }
            checks.check(owner.fixture_peer(0));
            {
                let mut view = owner.io();
                let mut bytes = [0; 8];
                checks.check(view.read(&mut bytes).unwrap() == 4 && &bytes[..4] == b"obs\n");
                checks.check(view.write(b"ctl\n").unwrap() == 4);
            }
            checks.check(owner.fixture_peer(1));
            {
                let mut view = owner.io();
                checks.check(view.half_close_control().is_ok());
                checks.check(view.half_close_control().is_ok());
                checks.check(view.write(b"x").unwrap_err().kind() == ErrorKind::BrokenPipe);
                checks.check(view.flush().is_ok());
            }
            checks.check(NativePreparation::fixture_counts().closes == 1);
            checks.check(owner.fixture_peer(3));
            checks.check(owner.state() == S::OwnedUnlaunched);
            checks.check(owner.fixture_peer(2));
            checks.check(owner.io().read(&mut [0]).unwrap() == 0);
            checks.check(owner.state() == S::OwnedUnlaunched);
            checks.check(owner.retire() == S::RetiredUnlaunched);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
        }
        "byte-unknown" => {
            use std::io::{ErrorKind, Read};
            let mut owner = owned(F::LostCloseReceipt);
            {
                let mut view = owner.io();
                checks.check(view.half_close_control().unwrap_err().kind() == ErrorKind::Other);
                checks.check(view.read(&mut [0]).unwrap_err().kind() == ErrorKind::NotConnected);
            }
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 0,
                    },
            );
            checks.check(owner.state() == S::Unknown);
            checks.check(owner.retire() == S::Unknown);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 0,
                    },
            );
            drop(owner);
            checks.check(matches!(
                NativePreparation::fixture(F::Normal),
                Preparation::Refused
            ));
        }
        "byte-reject" => {
            let mut owner = owned(F::Normal);
            checks.check(owner.fixture_reject_bad_io());
            checks.check(owner.fixture_probe());
            checks.check(owner.state() == S::OwnedUnlaunched);
            checks.check(owner.retire() == S::RetiredUnlaunched);
            checks.check(
                NativePreparation::fixture_counts()
                    == Counts {
                        open: 0,
                        closes: 4,
                        retire_calls: 1,
                    },
            );
        }
        _ => panic!("unknown fixture mode"),
    }
    checks.check(NativePreparation::fixture_counts().open == 0);
}

fn main() {
    let arguments: Vec<_> = std::env::args().collect();
    assert_eq!(arguments.len(), 2);
    let mode = &arguments[1];
    let mut checks = Checks(0);
    checks.check(matches!(NativePreparation::prepare(), Preparation::Refused));
    #[cfg(resident_launch_fixture)]
    fixture(mode, &mut checks);
    #[cfg(not(resident_launch_fixture))]
    assert_eq!(mode, "normal");
    println!(
        "NATIVE_RUST_OWNER mode={mode} passed={} failed=0 launched=0",
        checks.0
    );
}

#[cfg(not(resident_launch_fixture))]
fn main() {
    use resident_preparation::{NativePreparation, Preparation};
    unsafe extern "C" {
        fn rh_resident_preparation_exit(handle: u64) -> u64;
        fn rh_resident_preparation_retire_active_io(handle: u64) -> u32;
        fn rh_resident_preparation_abandon_active(handle: u64) -> u32;
        fn rh_resident_preparation_finish_active(handle: u64) -> u32;
    }
    let mut passed = 0;
    macro_rules! check {
        ($value:expr) => {
            assert!($value);
            passed += 1;
        };
    }
    check!(matches!(NativePreparation::prepare(), Preparation::Refused));
    // SAFETY: invalid zero handles must reject without acquiring any owner.
    unsafe {
        check!(rh_resident_preparation_exit(0) == 0);
        check!(rh_resident_preparation_retire_active_io(0) == 0);
        check!(rh_resident_preparation_abandon_active(0) == 0);
        check!(rh_resident_preparation_finish_active(0) == 0);
    }
    println!(
        "NATIVE_ACTIVE_BRIDGE mode=normal passed={passed} failed=0 childrenStarted=0 childrenWaited=0 open=0 unknown=0 producer=0"
    );
}
#[cfg(resident_launch_fixture)]
fn main() {
    exercised::run();
}
#[cfg(resident_launch_fixture)]
mod exercised {
    use resident_preparation::{
        ExitObservation as E, FixedActiveFixture as F, NativePreparation as N, Preparation as P,
        State as S,
    };
    use std::io::{ErrorKind, Read, Write};

    #[derive(Default)]
    struct Checks(u32);
    impl Checks {
        fn check(&mut self, value: bool) {
            assert!(value, "active bridge condition {} failed", self.0 + 1);
            self.0 += 1;
        }
    }
    fn pause() {
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
    fn owned(mode: F) -> N {
        match N::fixture_active(mode) {
            P::Owned(owner) => owner,
            _ => panic!("active owner refused"),
        }
    }
    fn wait(owner: &mut N) -> E {
        loop {
            let result = owner.poll_exit();
            if result != E::Pending {
                return result;
            }
            pause();
        }
    }
    fn collect() -> E {
        loop {
            let result = N::fixture_collect_retained_active();
            if result != E::Pending {
                return result;
            }
            pause();
        }
    }
    fn read(owner: &mut N, line: bool) -> Vec<u8> {
        let mut output = Vec::new();
        let mut io = owner.io();
        let mut byte = [0];
        loop {
            match io.read(&mut byte) {
                Ok(0) => return output,
                Ok(1) => {
                    output.push(byte[0]);
                    if line && byte[0] == b'\n' {
                        return output;
                    }
                }
                Err(e) if e.kind() == ErrorKind::WouldBlock => pause(),
                result => panic!("owned read {result:?}"),
            }
        }
    }
    fn collected(c: &mut Checks) {
        c.check(N::fixture_counts().open == 0);
        c.check(N::fixture_counts().closes == 4);
        c.check(N::fixture_counts().retire_calls == 0);
        c.check(N::fixture_active_stats().waits == 1);
        c.check(N::fixture_active_stats().abandons == 1);
        c.check(N::fixture_retained_state() == 3);
        c.check(matches!(N::prepare(), P::Refused));
        c.check(matches!(N::fixture_active(F::Echo), P::Refused));
    }
    fn child(args: &[String]) {
        assert_eq!(args.len(), 3);
        let description: Vec<_> = args[2].split(',').collect();
        assert_eq!(description.len(), 4);
        for end in description {
            assert_eq!(
                end.split(':').filter(|x| x.parse::<u64>().is_ok()).count(),
                3
            );
        }
        std::io::stdout().write_all(b"inherit-ok\n").unwrap();
        std::io::stdout().flush().unwrap();
        match args[1].as_str() {
            "early" => std::process::exit(0),
            "exit-two" => std::process::exit(2),
            "signal" => {
                unsafe extern "C" {
                    fn raise(signal: i32) -> i32;
                }
                // SAFETY: fixed test child signals only itself, never cleanup/kill.
                unsafe {
                    raise(15);
                }
                std::process::exit(89);
            }
            "echo" => {
                let mut input = Vec::new();
                std::io::stdin().read_to_end(&mut input).unwrap();
                if input != b"ctl\n" {
                    std::process::exit(88);
                }
                std::io::stdout().write_all(b"obs\n").unwrap();
            }
            _ => std::process::exit(78),
        }
    }
    fn active(mode: &str, c: &mut Checks) {
        let selected = match mode {
            "echo" => F::Echo,
            "lost" => F::LostCloseReceipt,
            "exit-two" => F::ExitTwo,
            "signal" => F::Signal,
            _ => F::Early,
        };
        let mut owner = match N::fixture_active(selected) {
            P::Owned(owner) if mode != "lost" => owner,
            P::Unknown(owner) if mode == "lost" => owner,
            _ => panic!("wrong active disposition"),
        };
        let expected = if mode == "lost" {
            S::Unknown
        } else {
            S::Active
        };
        c.check(owner.state() == expected);
        c.check(owner.fixture_finish_rejected());
        c.check(owner.retire() == expected); // Never the prelaunch retire syscall.
        c.check(owner.retire_active_io() == expected); // No observed Wait yet.
        c.check(N::fixture_counts().open == 2 && N::fixture_counts().closes == 2);
        c.check(owner.fixture_foreign_active_calls());
        c.check(N::fixture_active_stats().waits == 0);
        c.check(N::fixture_active_stats().abandons == 0);
        c.check(matches!(N::prepare(), P::Refused));
        if mode == "missing" {
            c.check(owner.io().read(&mut []).unwrap() == 0); // Not a raw EOF.
        } else if mode != "lost" {
            c.check(read(&mut owner, true) == b"inherit-ok\n");
            if mode == "echo" {
                owner.io().write_all(b"ctl\n").unwrap();
                c.check(owner.io().half_close_control().is_ok());
                c.check(owner.io().half_close_control().is_ok());
                c.check(read(&mut owner, false) == b"obs\n");
            } else if mode != "epipe" {
                c.check(owner.io().half_close_control().is_ok());
                c.check(read(&mut owner, false).is_empty());
            }
        }
        let exit = match mode {
            "exit-two" => E::Exit(2),
            "signal" => E::Signal(15),
            _ => E::Exit(0),
        };
        c.check(wait(&mut owner) == exit);
        c.check(owner.poll_exit() == exit);
        c.check(N::fixture_active_stats().waits == 1);
        if mode == "epipe" {
            c.check(owner.io().write(b"ctl\n").unwrap_err().kind() == ErrorKind::Other);
            c.check(owner.state() == S::Unknown);
            c.check(owner.io().read(&mut [0]).unwrap_err().kind() == ErrorKind::NotConnected);
        }
        let retired = if ["echo", "exit-two", "signal"].contains(&mode) {
            S::ActiveIoRetired
        } else {
            S::Unknown
        };
        c.check(owner.retire_active_io() == retired);
        if retired == S::Unknown {
            c.check(owner.finish_active().is_none());
        }
        c.check(owner.state() == retired);
        c.check(owner.poll_exit() == exit); // Exit never clears IO/owner unknown.
        c.check(owner.state() == retired);
        c.check(owner.retire_active_io() == retired);
        c.check(owner.retire() == retired);
        c.check(N::fixture_counts().open == 0 && N::fixture_counts().closes == 4);
        c.check(N::fixture_counts().retire_calls == 0);
        c.check(matches!(N::fixture_active(F::Echo), P::Refused)); // Even success retains native record.
        drop(owner);
        collected(c);
    }
    fn terminal(c: &mut Checks) {
        // Join includes TLS destruction. The original owner terminalizes on its
        // worker; no owner/Process migrates to this observing parent.
        let count = std::thread::spawn(|| {
            let mut c = Checks::default();
            let mut owner = owned(F::Echo);
            c.check(owner.fixture_finish_rejected());
            c.check(owner.finish_active().is_none());
            c.check(read(&mut owner, true) == b"inherit-ok\n");
            owner.io().write_all(b"ctl\n").unwrap();
            c.check(owner.io().half_close_control().is_ok());
            c.check(read(&mut owner, false) == b"obs\n");
            c.check(owner.fixture_finish_rejected()); // EOF without exact Wait.
            c.check(wait(&mut owner) == E::Exit(0));
            c.check(owner.fixture_finish_rejected()); // Wait without IO retirement.
            c.check(owner.retire_active_io() == S::ActiveIoRetired);
            c.check(owner.fixture_foreign_active_calls()); // Includes finalization.
            let proof = owner.finish_active();
            c.check(proof.is_some());
            c.check(owner.state() == S::Terminal);
            c.check(owner.finish_active().is_none());
            c.check(owner.fixture_finish_rejected()); // Native one-shot, not just Rust.
            c.check(owner.io().read(&mut [0]).unwrap_err().kind() == ErrorKind::NotConnected);
            c.check(matches!(N::fixture_active(F::Early), P::Refused));
            drop(owner);
            c.check(N::fixture_retained_state() == 7);
            c.check(N::fixture_active_stats().abandons == 0);
            c.check(N::fixture_active_stats().waits == 1);
            c.0
        })
        .join()
        .unwrap();
        c.0 += count;
        c.check(N::fixture_retained_state() == 7); // TLS did not poison terminal.
        c.check(N::fixture_active_stats().abandons == 0);
        c.check(N::fixture_counts().open == 0);
        c.check(N::fixture_counts().closes == 4);
        c.check(matches!(N::prepare(), P::Refused));
        c.check(matches!(N::fixture_active(F::Early), P::Refused));
        c.check(N::fixture_collect_retained_active() == E::Unavailable);
    }
    fn incomplete(mode: &str, c: &mut Checks) {
        if mode == "thread-exit" {
            std::thread::spawn(|| {
                let result = std::panic::catch_unwind(|| {
                    let mut owner = owned(F::Early);
                    assert_eq!(read(&mut owner, false), b"inherit-ok\n");
                    assert_eq!(wait(&mut owner), E::Exit(0));
                    std::mem::forget(owner); // TLS must act on the original worker.
                });
                if result.is_err() && N::fixture_retained_state() == 3 {
                    let _ = collect();
                }
                result.unwrap();
            })
            .join()
            .unwrap();
            c.check(N::fixture_collect_retained_active() == E::Unavailable); // Foreign TLS cannot collect.
        } else {
            let body = || {
                let mut owner = owned(F::Echo);
                assert_eq!(read(&mut owner, true), b"inherit-ok\n");
                assert_eq!(owner.poll_exit(), E::Pending);
                if mode == "unwind" {
                    panic!("fixed fixture unwind");
                }
                drop(owner); // Abandon releases control; same child exits88, no kill.
            };
            if mode == "unwind" {
                std::panic::set_hook(Box::new(|_| {}));
                c.check(std::panic::catch_unwind(body).is_err());
            } else {
                body();
            }
            c.check(collect() == E::Exit(88)); // Same retained record + exact Wait.
        }
        collected(c);
    }
    fn guards(c: &mut Checks) {
        c.check(matches!(N::prepare(), P::Refused));
        let mut owner = match N::fixture(resident_preparation::FixedFixture::Normal) {
            P::Owned(owner) => owner,
            _ => panic!("prelaunch"),
        };
        owner.request_active_close();
        c.check(owner.state() == S::OwnedUnlaunched);
        c.check(owner.poll_exit() == E::Unavailable);
        c.check(owner.retire_active_io() == S::OwnedUnlaunched);
        c.check(owner.fixture_reject_prelaunch_active_calls());
        c.check(N::fixture_active_stats().waits == 0 && N::fixture_active_stats().abandons == 0);
        c.check(N::fixture_counts().open == 4);
        c.check(owner.retire() == S::RetiredUnlaunched);
        c.check(N::fixture_counts().open == 0);
        c.check(N::fixture_retained_state() == 0);
    }
    fn decoder(c: &mut Checks) {
        for (raw, expected) in [
            (0, E::Unavailable),
            (1 << 32, E::Pending),
            (2 << 32, E::Exit(0)),
            ((2 << 32) | 2, E::Exit(2)),
            ((3 << 32) | 15, E::Signal(15)),
            (4 << 32, E::Unknown),
            (5 << 32, E::Unknown),
            ((1 << 32) | 1, E::Unknown),
            ((2 << 32) | 256, E::Unknown),
            ((2 << 32) | 0xffff_ffff, E::Unknown),
            ((2 << 32) | 255, E::Exit(255)),
            ((3 << 32) | 31, E::Signal(31)),
            (3 << 32, E::Unknown),
            ((3 << 32) | 32, E::Unknown),
            (u64::MAX, E::Unknown),
        ] {
            c.check(E::fixture_decode(raw) == expected);
        }
    }
    pub fn run() {
        let args: Vec<_> = std::env::args().skip(1).collect();
        if args.first().map(String::as_str) == Some("active-child") {
            child(&args);
            return;
        }
        assert_eq!(args.len(), 1);
        let mode = args[0].as_str();
        let mut checks = Checks::default();
        let outcome = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| match mode {
            "guards" => {
                guards(&mut checks);
                0
            }
            "decoder" => {
                decoder(&mut checks);
                0
            }
            "terminal" => {
                terminal(&mut checks);
                1
            }
            "drop" | "unwind" | "thread-exit" => {
                incomplete(mode, &mut checks);
                1
            }
            "echo" | "epipe" | "missing" | "lost" | "exit-two" | "signal" => {
                active(mode, &mut checks);
                1
            }
            _ => panic!("unknown fixed mode"),
        }));
        let child = match outcome {
            Ok(child) => child,
            Err(_) => {
                if N::fixture_retained_state() == 3 {
                    let _ = collect();
                }
                eprintln!(
                    "NATIVE_ACTIVE_BRIDGE_FAILED waits={} open={}",
                    N::fixture_active_stats().waits,
                    N::fixture_counts().open
                );
                std::process::exit(1);
            }
        };
        let unknown = if mode == "terminal" { 0 } else { child };
        println!(
            "NATIVE_ACTIVE_BRIDGE mode={mode} passed={} failed=0 childrenStarted={child} childrenWaited={child} open=0 unknown={unknown} producer=0",
            checks.0
        );
    }
}

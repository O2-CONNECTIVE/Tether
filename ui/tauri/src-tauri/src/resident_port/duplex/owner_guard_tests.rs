use super::*;
use std::mem::ManuallyDrop;
use std::os::unix::process::ExitStatusExt;

#[test]
fn retained_guard_releases_only_after_same_child_and_io_supervisor_join() {
    let command = super::super::owned_close_tests::command("confirmed");
    let (owner, source) = Supervisor::fixture_retained(command).unwrap();
    let mut owner = ManuallyDrop::new(owner);
    let first = source.and_then(|source| source.first_with_timeout(Duration::from_secs(2)));
    let before_join = owner.cleanup_confirmed();
    owner.request_close_transport();
    let start = std::time::Instant::now();
    let result = loop {
        if let Some(result) = owner.poll_join() {
            break result;
        }
        assert!(start.elapsed() < Duration::from_secs(10));
        thread::sleep(Duration::from_millis(5));
    };
    // Join the actual supervisor worker before assertions; success still needs
    // exact End::Reaped. A panic/unknown result cannot authorize guard release.
    // Unknown predicates are checked separately without parking the test parent.
    let confirmed = owner.cleanup_confirmed();
    let releasable = owner.can_release();
    if releasable {
        // SAFETY: this is the sole manual drop, after the worker was consumed.
        unsafe { ManuallyDrop::drop(&mut owner) };
    }
    assert!(first.is_ok());
    assert!(!before_join);
    assert!(matches!(result, Ok(End::Reaped(status)) if status.success()));
    assert!(confirmed && releasable);
}

#[test]
fn production_drop_guard_requires_proof_not_physical_reap_or_consumed_error() {
    // ManuallyDrop only avoids deliberately parking this predicate test forever.
    // No process or worker is created or abandoned by these state-only cases.
    let mut owner = ManuallyDrop::new(Supervisor {
        shared: Arc::new(Mutex::new(Shared::default())),
        worker: None,
        known: false,
        cleanup_confirmed: false,
        startup_rejected: false,
        not_spawned: false,
        require_cleanup: true,
    });
    assert!(!owner.can_release());
    owner.record(&Ok(End::Reaped(ExitStatus::from_raw(0))));
    assert!(!owner.can_release());
    owner.record(&Err(unavailable()));
    assert!(!owner.can_release());
    assert!(owner.poll_join().is_none());
    assert!(!owner.can_release());
    owner.record(&Ok(End::NotSpawned));
    assert!(owner.can_release());
    {
        let mut shared = owner.shared.lock().unwrap();
        shared.closure.request(100);
        shared.closure.half_closed();
        shared
            .closure
            .event(
                super::super::wire::Event::Closed {
                    protocol_version: 1,
                    source_continuous_ms: 100,
                    outcome: super::super::wire::CloseOutcome::Confirmed,
                },
                100,
            )
            .unwrap();
        shared.closure.eof();
    }
    owner.record(&Ok(End::Reaped(ExitStatus::from_raw(0))));
    assert!(owner.can_release());
    owner.record(&Ok(End::Reaped(ExitStatus::from_raw(256))));
    assert!(!owner.can_release());
    owner.record(&Err(unavailable()));
    assert!(!owner.can_release());
}

#[test]
fn clean_startup_rejection_is_failure_but_releasable_after_exact_join() {
    let script = r#"import ctypes,json,sys
lib=ctypes.CDLL('/usr/lib/libSystem.B.dylib')
class TB(ctypes.Structure): _fields_=[('n',ctypes.c_uint32),('d',ctypes.c_uint32)]
t=TB();assert lib.mach_timebase_info(ctypes.byref(t))==0
lib.mach_continuous_time.restype=ctypes.c_uint64
now=lib.mach_continuous_time()*t.n//t.d//1000000
print(json.dumps({'protocolVersion':1,'type':'startupRejected','sourceContinuousMs':now,'code':'policy-rejected'}),flush=True)
sys.exit(2)
"#;
    let mut cmd = std::process::Command::new("/usr/bin/python3");
    cmd.args(["-c", script]).env_clear();
    let (owner, source) = Supervisor::fixture(cmd).unwrap();
    let mut owner = ManuallyDrop::new(owner);
    owner.require_cleanup = true;
    assert!(source
        .unwrap()
        .first_with_timeout(Duration::from_secs(2))
        .is_err());
    loop {
        if owner.poll_join().is_some() {
            break;
        }
        thread::sleep(Duration::from_millis(5));
    }
    assert!(!owner.cleanup_confirmed());
    assert!(
        owner.can_release(),
        "clean pre-owner rejection must release only after join"
    );
    assert!(owner.startup_rejected());
    owner.record(&Err(unavailable()));
    assert!(!owner.can_release());
    assert!(!owner.startup_rejected());
}
#[test]
fn strict_decoder_accepts_typed_startup_rejection() {
    assert!(super::super::wire::Event::parse(br#"{"protocolVersion":1,"type":"startupRejected","sourceContinuousMs":1,"code":"policy-rejected"}"#).is_ok());
}

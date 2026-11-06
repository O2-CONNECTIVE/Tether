//! Real pipe fixtures execute the selected Python image + native clock, no host/backend.
use super::{
    closure::Closure,
    owner::{End, Supervisor},
    test_frames::status_line as status,
    wire::Event,
};
use serde_json::json;
use std::{
    process::Command,
    thread,
    time::{Duration, Instant},
};
const SCRIPT: &str = r#"
import sys,time,os
print('RESIDENT_FIXTURE_CHILD phase=entered pid='+str(os.getpid())+' mode='+sys.argv[1],file=sys.stderr,flush=True)
assert os.path.realpath(sys.executable)==os.path.realpath(sys.argv[3]), "fixture must execute the selected Python image directly"
import ctypes,json
lib=ctypes.CDLL('/usr/lib/libSystem.B.dylib')
class Timebase(ctypes.Structure): _fields_=[('numer',ctypes.c_uint32),('denom',ctypes.c_uint32)]
tb=Timebase(); assert lib.mach_timebase_info(ctypes.byref(tb))==0
lib.mach_continuous_time.restype=ctypes.c_uint64
now=lambda:lib.mach_continuous_time()*tb.numer//tb.denom//1000000
mode=sys.argv[1]
s=json.loads(sys.argv[2]); s['sourceContinuousMs']=now()
print('RESIDENT_FIXTURE_CHILD phase=emit pid='+str(os.getpid())+' native_ms='+str(s['sourceContinuousMs']),file=sys.stderr,flush=True)
print(json.dumps(s),flush=True)
if mode!='unsolicited': sys.stdin.read()
if mode=='delayed': time.sleep(3.1)
f={'protocolVersion':1,'type':'closed','sourceContinuousMs':now(),'outcome':'unknown' if mode=='unknown' else 'confirmed'}
if mode=='malformed': print('{',flush=True)
elif mode=='truncated': sys.stdout.write(json.dumps(f)); sys.stdout.flush()
elif mode!='missing': print(json.dumps(f),flush=True)
if mode=='duplicate': print(json.dumps(f),flush=True)
if mode=='extra': print(json.dumps(s),flush=True)
if mode=='hold': time.sleep(0.2)
sys.exit(1 if mode=='exit-error' else 0)
"#;
pub(super) fn command(mode: &str) -> Command {
    // Resolve the developer-tool shim before the lifecycle begins. This does
    // not run or warm Python; every actual child still has the same deadline.
    let image = super::fixture_python::image();
    let mut command = Command::new(&image);
    command
        .args(["-I", "-S", "-c", SCRIPT, mode, &status(1, 0, json!(null))])
        .arg(&image)
        .env_clear();
    command
}
fn launch(mode: &str) -> (Supervisor, super::owner::BoundDuplex) {
    let (owner, source) = Supervisor::fixture(command(mode)).unwrap();
    (owner, source.unwrap())
}
fn join(owner: &mut Supervisor) {
    let start = Instant::now();
    loop {
        if let Some(result) = owner.poll_join() {
            assert!(matches!(result.unwrap(), End::Reaped(_)));
            return;
        }
        assert!(start.elapsed() < Duration::from_secs(10));
        thread::sleep(Duration::from_millis(5));
    }
}
#[test]
fn fresh_actual_close_proof_survives_old_status_freshness_then_requires_join() {
    let (mut owner, source) = launch("delayed");
    source.first_with_timeout(Duration::from_secs(2)).unwrap();
    owner.request_close_transport();
    assert!(!owner.cleanup_confirmed());
    join(&mut owner);
    assert!(owner.cleanup_confirmed());
    assert_eq!(source.current().unwrap().valid_for_ms, 0);
}
#[test]
fn terminal_record_alone_before_eof_and_reap_cannot_confirm() {
    let (mut owner, source) = launch("hold");
    source.first_with_timeout(Duration::from_secs(2)).unwrap();
    owner.request_close_transport();
    thread::sleep(Duration::from_millis(50));
    assert!(owner.poll_join().is_none());
    assert!(!owner.cleanup_confirmed());
    join(&mut owner);
    assert!(owner.cleanup_confirmed());
}
#[test]
fn unknown_duplicate_following_frame_missing_truncated_and_exit_error_never_confirm() {
    for mode in [
        "unknown",
        "duplicate",
        "extra",
        "missing",
        "truncated",
        "malformed",
        "exit-error",
    ] {
        let (mut owner, source) = launch(mode);
        source.first_with_timeout(Duration::from_secs(2)).unwrap();
        owner.request_close_transport();
        join(&mut owner);
        assert!(!owner.cleanup_confirmed(), "confirmed {mode}");
    }
}
#[test]
fn no_output_child_still_hits_readiness_deadline_and_reap_is_not_cleanup() {
    let mut command = Command::new("/bin/sh");
    command
        .args(["-c", "IFS= read -r line; exit 0"])
        .env_clear();
    let (mut owner, source) = Supervisor::fixture(command).unwrap();
    let source = source.unwrap();
    assert!(source
        .first_with_timeout(Duration::from_millis(20))
        .is_err());
    join(&mut owner);
    assert!(source.current().is_none());
    assert!(!owner.cleanup_confirmed());
}

#[test]
fn unsolicited_terminal_is_not_a_close_proof() {
    let (mut owner, source) = launch("unsolicited");
    let _ = source.first_with_timeout(Duration::from_secs(1));
    join(&mut owner);
    assert!(!owner.cleanup_confirmed());
}
#[test]
fn close_proof_clock_and_shutdown_ignore_ordinary_freshness() {
    let mut c = Closure::default();
    c.request(100);
    c.half_closed();
    c.event(
        Event::parse(status(1, 0, json!(null)).as_bytes()).unwrap(),
        75_000,
    )
    .unwrap();
    let close = |time| {
        serde_json::to_vec(&json!({"protocolVersion":1,"type":"closed","sourceContinuousMs":time,"outcome":"confirmed"})).unwrap()
    };
    for bad in [99, 75_001, 72_000] {
        let mut invalid = Closure::default();
        invalid.request(100);
        invalid.half_closed();
        assert!(invalid
            .event(Event::parse(&close(bad)).unwrap(), 75_000)
            .is_err());
        assert!(invalid
            .event(Event::parse(&close(75_000)).unwrap(), 75_000)
            .is_err());
        invalid.eof();
        assert!(!invalid.confirmed());
    }
    c.event(Event::parse(&close(75_000)).unwrap(), 75_000)
        .unwrap();
    assert!(!c.confirmed());
    c.eof();
    assert!(c.confirmed());
}

#[test]
fn shutdown_source_and_native_clock_cannot_regress() {
    for (prior, source, now) in [(200, 150, 200), (100, 150, 150)] {
        let mut c = Closure::default();
        c.request(100);
        c.half_closed();
        c.event(
            Event::parse(status(1, prior, json!(null)).as_bytes()).unwrap(),
            200,
        )
        .unwrap();
        let terminal=serde_json::to_vec(&json!({"protocolVersion":1,"type":"closed","sourceContinuousMs":source,"outcome":"confirmed"})).unwrap();
        assert!(c.event(Event::parse(&terminal).unwrap(), now).is_err());
        c.eof();
        assert!(!c.confirmed());
    }
}

#[test]
fn native_clock_regression_after_terminal_before_eof_invalidates_proof() {
    let mut c = Closure::default();
    c.request(100);
    c.half_closed();
    let terminal=serde_json::to_vec(&json!({"protocolVersion":1,"type":"closed","sourceContinuousMs":200,"outcome":"confirmed"})).unwrap();
    c.event(Event::parse(&terminal).unwrap(), 200).unwrap();
    assert!(c.tick(150).is_err());
    c.eof();
    assert!(!c.confirmed());
}

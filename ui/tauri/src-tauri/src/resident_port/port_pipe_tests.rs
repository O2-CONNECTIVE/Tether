//! Synthetic source-only fixture. No producer identity or authentication claim.
use super::*;
use crate::standby_lifecycle::{Outcome, StandbyOwner};
use serde_json::{json, Value};
use std::{
    fs,
    path::PathBuf,
    process::Command,
    thread,
    time::{Duration, Instant},
};

const SCRIPT: &str = r#"
import ctypes,json,os,sys,time
lib=ctypes.CDLL('/usr/lib/libSystem.B.dylib')
class TB(ctypes.Structure): _fields_=[('n',ctypes.c_uint32),('d',ctypes.c_uint32)]
t=TB();assert lib.mach_timebase_info(ctypes.byref(t))==0
lib.mach_continuous_time.restype=ctypes.c_uint64
now=lambda:lib.mach_continuous_time()*t.n//t.d//1000000
s={'protocolVersion':1,'source':'resident-owner','state':'source-observation','revision':1,'validForMs':3000,'standby':'off','registration':'disconnected','caller':None,'work':'idle','cleanup':'confirmed','canRequestOn':True,'canRequestOff':False,'transition':None}
def status(): print(json.dumps({'protocolVersion':1,'type':'status','sourceContinuousMs':now(),'snapshot':s}),flush=True)
status()
for i,line in enumerate(sys.stdin):
 r=json.loads(line)['request']
 print(json.dumps({'protocolVersion':1,'type':'receipt','receipt':{'protocolVersion':1,'requestId':r['requestId'],'intent':r['intent'],'disposition':'accepted'}}),flush=True)
 while not os.path.exists(os.path.join(sys.argv[1],str(i))): time.sleep(0.002)
 s.update(revision=s['revision']+1,standby='on' if r['intent']=='on' else 'off',canRequestOn=r['intent']=='off',canRequestOff=r['intent']=='on',transition={'requestId':r['requestId'],'intent':r['intent'],'outcome':'applied'})
 status()
print(json.dumps({'protocolVersion':1,'type':'closed','sourceContinuousMs':now(),'outcome':'confirmed'}),flush=True)
"#;

struct OwnedDirectory(PathBuf);
impl OwnedDirectory {
    fn new() -> Self {
        static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let number = NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let path =
            std::env::temp_dir().join(format!("rescue-port-test-{}-{number}", std::process::id()));
        // Never delete/reuse a pre-existing directory that this fixture didn't create.
        fs::create_dir(&path).unwrap();
        Self(path)
    }
    fn release(&self, phase: usize) {
        fs::write(self.0.join(phase.to_string()), []).unwrap();
    }
}
impl Drop for OwnedDirectory {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}
struct Fixture {
    directory: OwnedDirectory,
    owner: Option<StandbyOwner>,
    port: ResidentPort,
}
impl Fixture {
    fn start() -> Self {
        let directory = OwnedDirectory::new();
        let mut command = Command::new("/usr/bin/python3");
        command.args(["-c", SCRIPT]).arg(&directory.0).env_clear();
        let (producer, source) = duplex::owner::Supervisor::fixture_retained(command).unwrap();
        let owner = StandbyOwner::start(producer).unwrap();
        let port = ResidentPort::bound(source.unwrap()).unwrap();
        Self {
            directory,
            owner: Some(owner),
            port,
        }
    }
    fn status(&self) -> Value {
        serde_json::to_value(self.port.current()).unwrap()
    }
    fn request(&self, id: u64, revision: u64, intent: &str) -> Result<Value, String> {
        let request = serde_json::from_value(
            json!({"protocolVersion":1,"requestId":id,"expectedRevision":revision,"intent":intent}),
        )
        .unwrap();
        let receive = self.port.request(request).map_err(str::to_owned)?;
        receive
            .recv_timeout(Duration::from_secs(3))
            .map_err(|e| e.to_string())
            .and_then(|receipt| serde_json::to_value(receipt).map_err(|e| e.to_string()))
    }
    fn revision(&self, revision: u64) -> Result<Value, &'static str> {
        let start = Instant::now();
        loop {
            let status = self.status();
            if status["revision"] == revision {
                return Ok(status);
            }
            if start.elapsed() >= Duration::from_secs(3) {
                return Err("fixture observation timeout");
            }
            thread::sleep(Duration::from_millis(2));
        }
    }
    fn finish(&mut self) -> Option<Outcome> {
        // Release every test-only barrier even when an earlier observation failed.
        self.directory.release(0);
        self.directory.release(1);
        self.owner.take().map(|mut owner| {
            owner.join(); // real Close/EOF/Child/IO worker AND outer monitor join
            let outcome = owner.gate.outcome();
            assert!(owner.gate.request_exit());
            outcome.unwrap()
        })
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        self.finish();
    }
}

#[test]
fn accepted_on_and_off_receipts_do_not_replace_matching_owner_observations() {
    let mut fixture = Fixture::start();
    let before_on = fixture.status();
    let on = fixture.request(1, 1, "on");
    let after_on_receipt = fixture.status();
    let overlapping = fixture.request(2, 1, "on");
    fixture.directory.release(0);
    let applied_on = fixture.revision(2);
    let off = fixture.request(3, 2, "off");
    let after_off_receipt = fixture.status();
    fixture.directory.release(1);
    let applied_off = fixture.revision(3);
    let cleanup = fixture.finish();
    let ended = fixture.status();
    let after_close = fixture.request(4, 3, "on");
    let directory = fixture.directory.0.clone();
    drop(fixture); // all owned resources and the fixture directory before asserts

    assert_eq!(on.unwrap()["disposition"], "accepted");
    assert_eq!(off.unwrap()["disposition"], "accepted");
    assert_eq!(overlapping.unwrap()["disposition"], "rejected");
    for key in [
        "revision",
        "standby",
        "registration",
        "caller",
        "work",
        "cleanup",
        "transition",
        "canRequestOn",
        "canRequestOff",
    ] {
        assert_eq!(
            before_on[key], after_on_receipt[key],
            "ON receipt changed {key}"
        );
        assert_eq!(
            applied_on.as_ref().unwrap()[key],
            after_off_receipt[key],
            "OFF receipt changed {key}"
        );
    }
    let applied_on = applied_on.unwrap();
    let applied_off = applied_off.unwrap();
    assert_eq!(applied_on["standby"], "on");
    assert_eq!(
        applied_on["transition"],
        json!({"requestId":1,"intent":"on","outcome":"applied"})
    );
    assert_eq!(applied_off["standby"], "off");
    assert_eq!(applied_off["registration"], "disconnected");
    assert_eq!(
        applied_off["transition"],
        json!({"requestId":3,"intent":"off","outcome":"applied"})
    );
    assert_eq!(cleanup, Some(Outcome::Confirmed));
    assert_eq!(ended["validForMs"], 0);
    assert_eq!(ended["canRequestOn"], false);
    assert_eq!(ended["canRequestOff"], false);
    assert_eq!(after_close.unwrap()["disposition"], "rejected");
    assert!(!directory.exists());
}

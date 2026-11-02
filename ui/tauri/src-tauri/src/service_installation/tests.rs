use super::*;
use std::collections::BTreeMap;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::{atomic::{AtomicUsize, Ordering}, mpsc, Arc, Mutex};
use std::thread;
use std::time::Duration;

fn seed() -> Seed {
    Seed { service_name:"FixtureService", service_sid:"S-1-5-80-1-2-3-4-5",
        operator_sid:"S-1-5-21-10-20-30-1001", guest_sid:"S-1-5-21-10-20-30-1002",
        record_path:r"C:\safe\record.json", digest_path:r"C:\safe\record.sha256",
        volume_root:r"C:\", filesystem:"NTFS", serial:7, read_ms:1000, cleanup_ms:1000,
        record:b"{}\n", digest:b"0000000000000000000000000000000000000000000000000000000000000000\n" }
}
fn principal() -> Principal {
    Principal { user:seed().operator_sid.into(), kind:1, token_id:1, authentication_id:2,
        modified_id:3, groups:vec![] }
}
#[derive(Default)]
struct State {
    next:usize, files:BTreeMap<usize,FileState>, closed:Vec<usize>, checks:usize,
    reads:usize, deny:bool, corrupt:bool, change_identity:bool, close_error:bool,
    panic_inspect:bool, panic_read:bool, raw_pending:usize, fatal:usize,
}
#[derive(Clone, Default)]
struct Fake { state:Arc<Mutex<State>>, dropped:Arc<AtomicUsize> }
struct Pending(Arc<AtomicUsize>);
impl Drop for Pending { fn drop(&mut self) { self.0.fetch_add(1,Ordering::SeqCst); } }
impl Calls for Fake {
    fn principal(&self)->Result<Principal,Fault> {
        let state=self.state.lock().unwrap();
        if state.deny { return Err(Fault::Denied); }
        let mut p=principal(); if state.change_identity { p.modified_id+=1; } Ok(p)
    }
    fn service_sid(&self,_:&str)->Result<String,Fault>{Ok(seed().service_sid.into())}
    fn open_token(&self)->Result<usize,Fault>{Ok(1)}
    fn token_principal(&self,_:usize)->Result<Principal,Fault>{self.principal()}
    fn open(&self,path:&str,leaf:bool)->Result<usize,Fault>{
        let mut s=self.state.lock().unwrap();s.next+=1;let h=s.next+10;
        let size=if path==seed().record_path{seed().record.len()}else if path==seed().digest_path{65}else{0};
        s.files.insert(h,FileState{path:path.into(),owner:"S-1-5-18".into(),filesystem:"NTFS".into(),
            volume:7,id:[h as u8;16],acl:vec![],disk:true,directory:!leaf,reparse:false,dacl:true,
            protected:true,links:1,size:size as u64});Ok(h)
    }
    fn inspect(&self,file:usize)->Result<FileState,Fault>{
        let mut s=self.state.lock().unwrap();s.checks+=1;let panic=s.panic_inspect;
        let value=s.files.get(&file).cloned().ok_or(Fault::Denied);drop(s);
        assert!(!panic,"synthetic partial acquisition panic");value
    }
    fn read(&self,file:usize,_:usize,_:&AtomicBool,_:Instant,_:Duration)->Result<Vec<u8>,Fault>{
        let mut s=self.state.lock().unwrap();s.reads+=1;
        if s.panic_read {
            s.raw_pending=Box::into_raw(Box::new(Pending(self.dropped.clone()))) as usize;
            drop(s);panic!("synthetic pending IO panic");
        }
        let path=&s.files.get(&file).ok_or(Fault::Denied)?.path;
        let mut bytes=if path==seed().record_path{seed().record.to_vec()}else{seed().digest.to_vec()};
        if s.corrupt{bytes[0]^=1;}Ok(bytes)
    }
    fn close(&self,h:usize)->Result<(),Fault>{
        let mut s=self.state.lock().unwrap();if s.close_error{return Err(Fault::Unknown)}
        s.closed.push(h);Ok(())
    }
    fn fatal(&self)->!{self.state.lock().unwrap().fatal+=1;panic!("synthetic fatal")}
}
fn until(owner:&mut Installation, predicate:impl Fn(Observation)->bool)->Observation{
    let deadline=Instant::now()+Duration::from_secs(1);
    loop{let value=owner.observe();if predicate(value){return value}
        assert!(Instant::now()<deadline,"finite synthetic worker did not respond");thread::yield_now();}
}
#[test]
fn absent_seed_has_no_worker_or_authority(){
    let mut owner=Installation::default();assert!(owner.worker.is_none());
    assert_eq!(owner.observe(),Observation::NotConfigured);
}
#[test]
fn invalid_seed_refuses_before_calls(){
    let fake=Fake::default();let mut bad=seed();bad.serial=0;
    let mut owner=Installation::with_calls(bad,fake.clone());
    assert_eq!(owner.observe(),Observation::Denied);assert_eq!(fake.state.lock().unwrap().next,0);
}
#[test]
fn normal_interface_acquires_once_rechecks_and_joins(){
    let fake=Fake::default();let mut owner=Installation::with_calls(seed(),fake.clone());
    let deadline=Instant::now()+Duration::from_secs(1);
    while { let state=fake.state.lock().unwrap(); state.reads<2 || state.checks<20 } {
        assert!(matches!(owner.observe(),Observation::Pending|Observation::Checked(_)));
        assert!(Instant::now()<deadline);thread::yield_now();
    }
    let opened=fake.state.lock().unwrap().next;
    drop(owner);let s=fake.state.lock().unwrap();assert_eq!(s.next,opened);
    assert_eq!(s.closed.len(),opened+1);assert_eq!(s.reads,2);assert_eq!(s.fatal,0);
}
#[test]
fn denial_is_sticky_and_never_reacquires(){
    let fake=Fake::default();fake.state.lock().unwrap().corrupt=true;
    let mut owner=Installation::with_calls(seed(),fake.clone());
    assert_eq!(until(&mut owner,|x|matches!(x,Observation::Denied|Observation::Unknown)),Observation::Denied);
    let opened=fake.state.lock().unwrap().next;fake.state.lock().unwrap().corrupt=false;
    for _ in 0..8{assert_eq!(owner.observe(),Observation::Denied)}
    drop(owner);assert_eq!(fake.state.lock().unwrap().next,opened);
}
#[test]
fn identity_change_after_acquisition_refuses_cached_success(){
    let fake=Fake::default();let mut owner=Installation::with_calls(seed(),fake.clone());
    let end=Instant::now()+Duration::from_secs(1);
    while fake.state.lock().unwrap().reads<2{owner.observe();assert!(Instant::now()<end);thread::yield_now();}
    fake.state.lock().unwrap().change_identity=true;
    assert_eq!(until(&mut owner,|x|matches!(x,Observation::Denied|Observation::Unknown)),Observation::Denied);
}
#[test]
fn unavailable_read_principal_never_falls_back(){
    let fake=Fake::default();fake.state.lock().unwrap().deny=true;
    let mut owner=Installation::with_calls(seed(),fake.clone());
    assert_eq!(until(&mut owner,|x|matches!(x,Observation::Denied|Observation::Unknown)),Observation::Denied);
    drop(owner);assert_eq!(fake.state.lock().unwrap().next,0);
}
#[test]
fn panic_guard_retains_partial_and_pending_resources_before_fatal(){
    for pending in [false,true]{
        let fake=Fake::default();{
            let mut s=fake.state.lock().unwrap();s.panic_inspect=!pending;s.panic_read=pending;
        }
        let mut owner=retained::Retained::new(seed(),fake.clone());
        let(tx,rx)=mpsc::sync_channel(1);let(send,_recv)=mpsc::sync_channel(1);tx.send(1).unwrap();
        let result=catch_unwind(AssertUnwindSafe(||worker::guarded(&mut owner,&AtomicBool::new(false),rx,send)));
        assert!(result.is_err());let s=fake.state.lock().unwrap();
        assert_eq!(s.fatal,1);assert!(s.closed.is_empty());assert_eq!(fake.dropped.load(Ordering::SeqCst),0);
        let raw=s.raw_pending;drop(s);
        if raw!=0{
            // Synthetic operation has no kernel reference. Explicit test-only
            // disposal happens AFTER observing fatal and preserved ownership.
            drop(unsafe{Box::from_raw(raw as *mut Pending)});
            assert_eq!(fake.dropped.load(Ordering::SeqCst),1);
        }
    }
}
#[test]
fn close_error_is_fatal_without_retry(){
    let fake=Fake::default();fake.state.lock().unwrap().close_error=true;
    let mut owner=retained::Retained::new(seed(),fake.clone());
    let(tx,rx)=mpsc::sync_channel(1);let(send,recv)=mpsc::sync_channel(1);tx.send(1).unwrap();drop(tx);
    assert!(catch_unwind(AssertUnwindSafe(||worker::guarded(&mut owner,&AtomicBool::new(false),rx,send))).is_err());
    assert!(recv.try_recv().unwrap().1.is_ok());let s=fake.state.lock().unwrap();
    assert_eq!(s.fatal,1);assert!(s.closed.is_empty());
}
#[test]
fn acl_matches_root_effective_write_policy(){
    let s=seed();let read=Ace{kind:0,flags:0,mask:0x120089,sid:s.operator_sid.into()};
    assert!(policy::safe_acl(&s,std::slice::from_ref(&read)));
    for mask in [0x2,0x4,0x10,0x40,0x10000,0x40000,0x80000,0x40000000,0x10000000]{
        let mut write=read.clone();write.mask=mask;
        assert!(!policy::safe_acl(&s,std::slice::from_ref(&write)));
        let mut deny=write.clone();deny.kind=1;
        assert!(!policy::safe_acl(&s,&[deny,write.clone()]));
        write.flags=8;assert!(policy::safe_acl(&s,&[write]));
    }
    for (kind,flags,mask) in [(2,0,1),(0,0x80,1),(0,0,0x02000000)]{
        let mut unknown=read.clone();unknown.kind=kind;unknown.flags=flags;unknown.mask=mask;
        assert!(!policy::safe_acl(&s,&[unknown]));
    }
}
#[test]
fn malformed_seed_and_conflicting_paths_refuse(){
    for change in 0..8{let mut s=seed();match change{
        0=>s.record_path=r"C:\safe\..\record",1=>s.digest_path=s.record_path,
        2=>s.volume_root=r"D:\",3=>s.operator_sid=s.guest_sid,4=>s.operator_sid="S-1-5-18",
        5=>s.read_ms=120001,6=>s.service_sid="S-1-5-80-1",_=>s.filesystem="FAT32"}
        assert!(!policy::valid_seed(&s));}
}

#[test]
fn generated_present_seed_cannot_activate_unadopted_native_policy() {
    let mut owner = Installation::from_generated(Some(seed()));
    assert!(owner.worker.is_none());
    assert_eq!(owner.observe(), Observation::NotConfigured);
}

#[test]
fn actual_shell_caller_consumes_denial_without_enabling_operations() {
    let normal = crate::shell_runtime::ShellRuntime::default();
    let status = normal.status().unwrap();
    assert_eq!(status.integration, "not_connected");
    assert!(!status.can_start && !status.can_stop);
    let fake = Fake::default();
    fake.state.lock().unwrap().deny = true;
    let runtime = crate::shell_runtime::ShellRuntime::with_installation_fixture(
        Installation::with_calls(seed(), fake));
    let deadline = Instant::now() + Duration::from_secs(1);
    loop {
        if runtime.status().is_err() { break; }
        assert!(Instant::now() < deadline);
        thread::yield_now();
    }
    assert!(runtime.status().is_err());
}
#[test]
fn file_protection_rejects_unknown_or_operator_mutation() {
    let fake = Fake::default();
    let h = fake.open(seed().record_path, true).unwrap();
    let original = fake.inspect(h).unwrap();
    assert!(policy::valid_file(&seed(), &original, seed().record_path, true));
    for mutation in 0..10 {
        let mut f = original.clone();
        match mutation {
            0 => f.owner = seed().operator_sid.into(),
            1 => f.dacl = false,
            2 => f.reparse = true,
            3 => f.directory = true,
            4 => f.volume += 1,
            5 => f.id = [0; 16],
            6 => f.links = 2,
            7 => f.path.push('x'),
            8 => f.filesystem = "FAT32".into(),
            _ => f.disk = false,
        }
        assert!(!policy::valid_file(&seed(), &f, seed().record_path, true));
    }
}

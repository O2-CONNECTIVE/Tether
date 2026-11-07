"""Real mpsc, actual pinned patched pump and actual recorder; finite mocked Win32 only."""
import json, os, subprocess, sys, tempfile, unittest, hashlib
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'scripts'))
import prepare_wry_delivery as prep
import wry_receiver_record as record
class ReceiverTests(unittest.TestCase):
    def test_actual_seam_and_negative_controls(self):
        archive=Path(os.environ['WRY_COMPLETION_CRATE']); rustc=os.environ['WRY_PROBE_RUSTC']
        original=prep.com_files(archive)['src/lib.rs'].decode()
        patched=prep.patch_com(original)
        self.assertEqual(prep.patch_com(patched,True),original)
        self.assertEqual(patched.count('rx.try_recv()'),1)
        self.assertEqual(patched.count('Pump::observe(rx.try_recv())'),1)
        runtime=(ROOT/'scripts/tests/fixtures/wry_delivery_runtime.rs').read_text()
        runtime=runtime.replace('    static MODE:', '    static ENTRIES: Cell<u8> = const { Cell::new(0) };\n    static MODE:')
        runtime=runtime.replace('        match MODE.with(Cell::get) {','        ENTRIES.with(|v| v.set(v.get()+1));\n        match MODE.with(Cell::get) {\n            6 => { invoke(); Bool(0) },')
        with tempfile.TemporaryDirectory() as d:
            d=Path(d); (d/'protocol.rs').write_bytes(prep.protocol_bytes()); (d/'pump.rs').write_text(patched[patched.index('pub fn wait_with_pump'):])
            recorder=json.dumps(str(ROOT/'ui/tauri/src-tauri/src/wry_delivery_probe.rs'))
            main=runtime+f'\n#[path={recorder}] mod recorder;\n'+r'''
fn main() {
 let case=std::env::args().nth(1).unwrap();
 let p=recorder::Probe::memory(); assert!(p.register());
 let scope=rescue_host_delivery::Scope::begin(1);
 let (tx,rx)=mpsc::channel::<u8>();
 let mut sender=Some(tx);
 if case=="ready" || case=="buffered-drop" { sender.as_ref().unwrap().send(7).unwrap(); }
 if case=="dropped" || case=="buffered-drop" { drop(sender.take()); }
 if case=="late-drop" { let tx=sender.take(); install(6,Box::new(move || drop(tx))); }
 else { install(5,Box::new(||{})); }
 let nested=if case=="nested" { Some(rescue_host_delivery::Scope::begin(2)) } else { None };
 let value=if case=="unbound" { wait_with_pump(rx) } else { scope.wait(rx) };
 let ready=case=="ready" || case=="buffered-drop";
 assert_eq!(value,if ready { Ok(7) } else { Err(Error::TaskCanceled) });
 assert_eq!(ENTRIES.with(Cell::get),if ready {0} else {1});
 print!("{}",p.receiver_snapshot(true,true));
}
'''
            (d/'main.rs').write_text(main)
            c=subprocess.run([rustc,'--edition=2024',str(d/'main.rs'),'-o',str(d/'run')],capture_output=True,timeout=30)
            self.assertEqual(c.returncode,0,c.stderr.decode())
            for case,state in [('ready',0),('buffered-drop',0),('empty',1),('dropped',2),('late-drop',1),('unbound',1),('nested',1)]:
                run=subprocess.run([str(d/'run'),case],capture_output=True,timeout=10); self.assertEqual(run.returncode,0,run.stderr.decode())
                v=record.validate(json.loads(run.stdout)); bucket=2 if case in ('unbound','nested') else 0
                self.assertEqual([v['buckets'][bucket][k] for k in record.COUNTS],[int(i==state) for i in range(3)])
                self.assertEqual(v['buckets'][bucket]['relation'],'unknown' if bucket==2 else 'original-wait')
                self.assertEqual(v['buckets'][1]['relation'],'unknown')
                bad=json.loads(run.stdout);bad['buckets'][2]['relation']='original-wait'
                with self.assertRaises(ValueError):record.validate(bad)
            # Closed-shape/privacy and missing is never live negative controls.
            for mutate in (lambda v: v.update(private='sentinel'),
                           lambda v: v['buckets'][1].update(relation='original-wait'),
                           lambda v: v['buckets'][0].update(emptyCapped=True)):
                bad=json.loads(run.stdout); mutate(bad)
                with self.assertRaises(ValueError): record.validate(bad)
            # A changed receive call count no longer inverse-projects exactly.
            mutated=patched.replace('Pump::observe(rx.try_recv())','Pump::observe({ let _ = rx.try_recv(); rx.try_recv() })')
            with self.assertRaises(ValueError): prep.patch_com(mutated,True)
            # Actual recorder's saturation/seal/interrupted tests, not truth flags.
            c=subprocess.run([rustc,'--edition=2024','--test',str(d/'main.rs'),'-o',str(d/'tests')],capture_output=True,timeout=30)
            self.assertEqual(c.returncode,0,c.stderr.decode())
            run=subprocess.run([str(d/'tests')],capture_output=True,timeout=10);self.assertEqual(run.returncode,0,run.stdout.decode())

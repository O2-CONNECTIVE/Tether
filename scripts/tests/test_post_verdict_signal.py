"""Actual bounded Rust file reader, not a reimplementation of admission."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class SignalTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.owner = tempfile.TemporaryDirectory(prefix='post-verdict-signal-')
        cls.directory = Path(cls.owner.name)
        source = cls.directory / 'main.rs'
        module = ROOT / 'ui/tauri/src-tauri/src/post_verdict_signal.rs'
        source.write_text('''#[path = %s] mod signal;
fn main() {
 let args: Vec<_> = std::env::args().collect();
 let path = std::path::Path::new(&args[1]);
 let mut reader = signal::Signal::new(path, &args[2]);
 let first = reader.poll();
 if args.len() > 3 { std::fs::copy(&args[3], path.join("post-verdict.signal")).unwrap(); }
 let second = reader.poll();
 println!("{} {}", first.is_some(), second.is_some());
 if let Some((manifest, failure)) = first { println!("{} {}", manifest, failure); }
}
''' % json.dumps(module.as_posix()))
        cls.exe = cls.directory / 'reader'
        rustc = os.environ['WRY_PROBE_RUSTC']
        result = subprocess.run([rustc, '--edition=2024', str(source), '-o', str(cls.exe)], capture_output=True, timeout=30)
        if result.returncode:
            raise AssertionError(result.stderr.decode())

        collector = cls.directory / 'collector.rs'
        collector.write_text('''#[path = %s] mod startup;
fn main() {
 let trace = startup::Trace::start();
 trace.record(startup::Stage::BuildStart);
 std::thread::sleep(std::time::Duration::from_millis(150));
 trace.record(startup::Stage::BuildError);
 let path = std::path::PathBuf::from(std::env::var_os("RESCUE_HOST_STARTUP_TRACE_DIR").unwrap());
 for _ in 0..100 { if path.join("app-startup-stages.json").is_file() { return; }
 std::thread::sleep(std::time::Duration::from_millis(10)); }
 panic!("collector did not finish");
}
''' % json.dumps((ROOT/'ui/tauri/src-tauri/src/startup_diagnostics.rs').as_posix()))
        cls.collector = cls.directory/'collector'
        result = subprocess.run([rustc, '--edition=2024', str(collector), '-o', str(cls.collector)], capture_output=True, timeout=30)
        if result.returncode:
            raise AssertionError(result.stderr.decode())

    @classmethod
    def tearDownClass(cls):
        cls.owner.cleanup()

    def setUp(self):
        self.case = tempfile.TemporaryDirectory(dir=self.directory)
        self.addCleanup(self.case.cleanup)
        self.root = Path(self.case.name)
        self.manifest = dict(protocol='post-verdict-manifest-1', run='123', attempt=1, trial=1,
                             sourceCommit='a'*40, sourceTree='b'*40, controlCommit='c'*40,
                             specimenSha256='d'*64, nonce='e'*64)
        self.signal = {key: self.manifest[key] for key in ('sourceCommit', 'sourceTree', 'controlCommit', 'specimenSha256')}
        self.signal.update(protocol='post-verdict-signal-1', manifestSha256='f'*64, failureSha256='0'*64)
        self.write('post-verdict.manifest.json', self.manifest)
        self.write('post-verdict.signal', self.signal)

    def write(self, name, value):
        (self.root/name).write_text(json.dumps(value, sort_keys=True, separators=(',', ':')))

    def run_reader(self, *extra):
        return subprocess.check_output([str(self.exe), str(self.root), 'f'*64, *map(str, extra)], timeout=5).decode().splitlines()

    def test_actual_reader_accepts_bound_signal_once(self):
        self.assertEqual(self.run_reader(), ['true false', 'f'*64+' '+'0'*64])

    def test_rejects_wrong_identity_closed_types_and_noncanonical_bytes(self):
        for key, value in [('sourceCommit', '0'*40), ('manifestSha256', '0'*64), ('failureSha256', True), ('extra', 1)]:
            with self.subTest(key=key):
                self.write('post-verdict.signal', {**self.signal, key: value})
                self.assertEqual(self.run_reader(), ['false false'])
        self.write('post-verdict.signal', self.signal)
        path = self.root/'post-verdict.signal'
        path.write_bytes(path.read_bytes()+b'\n')
        self.assertEqual(self.run_reader(), ['false false'])

    def test_partial_candidate_is_never_retried_after_replacement(self):
        self.write('valid.json', self.signal)
        (self.root/'post-verdict.signal').write_text('{')
        self.assertEqual(self.run_reader(self.root/'valid.json'), ['false false'])

    def test_duplicate_oversized_links_and_stale_manifest_rejected(self):
        path = self.root/'post-verdict.signal'
        valid = path.read_bytes()
        for data in (b'{"protocol":"post-verdict-signal-1",'+valid[1:], b' '*2049):
            path.write_bytes(data)
            self.assertEqual(self.run_reader(), ['false false'])
        path.write_bytes(valid)
        os.link(path, self.root/'hardlink')
        self.assertEqual(self.run_reader(), ['false false'])
        (self.root/'hardlink').unlink()
        path.rename(self.root/'target')
        path.symlink_to(self.root/'target')
        self.assertEqual(self.run_reader(), ['false false'])
        path.unlink(); path.write_bytes(valid)
        self.write('post-verdict.manifest.json', {**self.manifest, 'attempt': True})
        self.assertEqual(self.run_reader(), ['false false'])

    def test_actual_collector_accepts_signal_and_natural_finish_wins(self):
        env = {**os.environ, 'RESCUE_HOST_STARTUP_TRACE_DIR': str(self.root),
               'RESCUE_HOST_POST_VERDICT_MANIFEST_SHA256': 'f'*64}
        subprocess.run([str(self.collector)], env=env, check=True, timeout=5, capture_output=True)
        value = json.loads((self.root/'wry-post-verdict-probe.json').read_bytes())
        self.assertEqual(value['anchor']['state'], 'observed')
        self.assertEqual(value['anchor']['manifestSha256'], 'f'*64)
        for name in ('wry-post-verdict-probe.json', 'wry-completion-probe.json', 'wry-sdk-delivery.json',
                     'wry-receiver-polls.json', 'app-startup-stages.json'):
            (self.root/name).unlink()
        (self.root/'app-startup.finish').write_bytes(b'')
        subprocess.run([str(self.collector)], env=env, check=True, timeout=5, capture_output=True)
        value = json.loads((self.root/'wry-post-verdict-probe.json').read_bytes())
        self.assertEqual(value['anchor']['state'], 'missing')

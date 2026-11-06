"""Exact pinned loop with SIMULATED Win32 delivery. No SDK/Windows/app calls."""
from pathlib import Path
import argparse
import hashlib
import json
import subprocess
import tarfile
import tempfile

ARCHIVE_SHA = '3f89fca7a704cee10dcb3654c1dbb8941d1783132f1917358af75bec37a7d7e6'
LIB_SHA = 'de9ec5585a990725576de47557f93529cddbdf267ab3847835689e2a01c854fe'


def run(archive: Path, rustc: str) -> dict:
    assert hashlib.sha256(archive.read_bytes()).hexdigest() == ARCHIVE_SHA, 'ARCHIVE_PIN_REJECTED'
    with tarfile.open(archive) as bundle:
        source = bundle.extractfile('webview2-com-0.39.1/src/lib.rs').read()
    assert hashlib.sha256(source).hexdigest() == LIB_SHA, 'SOURCE_PIN_REJECTED'
    pump = source[source.index(b'pub fn wait_with_pump<T>'):]
    fixture_path = Path(__file__).parent / 'tests' / 'fixtures' / 'wry_pump_model.rs'
    fixture = fixture_path.read_bytes()
    poll = b'        if let Ok(result) = rx.try_recv() {\n            return Ok(result);\n        }\n'
    assert pump.count(poll) == 1 and pump.endswith(b'        }\n    }\n}\n')
    mutant = pump.replace(poll, b'', 1)
    tail = b'        }\n    }\n}\n'
    mutant = mutant[:-len(tail)] + b'        }\n' + poll + b'    }\n}\n'
    with tempfile.TemporaryDirectory(prefix='callback-pump-model-') as temp:
        root = Path(temp)
        (root / 'contract.rs').write_bytes(fixture)

        def compile_and_run(body: bytes, args: list[str]) -> subprocess.CompletedProcess:
            (root / 'pump.rs').write_bytes(body)
            build = subprocess.run(
                [rustc, '--edition=2021', '--test', str(root / 'contract.rs'),
                 '-o', str(root / 'model')], capture_output=True, timeout=20)
            assert build.returncode == 0, 'MODEL_COMPILE_REJECTED'
            return subprocess.run(
                [str(root / 'model'), '--test-threads=1', *args],
                capture_output=True, timeout=10)

        positive = compile_and_run(pump, [])
        assert positive.returncode == 0 and b'3 passed; 0 failed' in positive.stdout, 'MODEL_CASE_REJECTED'
        negative = compile_and_run(mutant, ['--exact', 'ready_is_checked_before_getmessage'])
        assert negative.returncode != 0 and b'0 passed; 1 failed' in negative.stdout, 'ORDER_MUTATION_NOT_DETECTED'
    return {'schema': 1, 'scope': 'simulated-delivery-exact-loop', 'archiveSha256': ARCHIVE_SHA,
            'libSha256': LIB_SHA, 'functionSha256': hashlib.sha256(pump).hexdigest(),
            'modelCasesPassed': 3, 'pollOrderMutationDetected': True,
            'windowsApiInvoked': False, 'webview2SdkInvoked': False,
            'originalTimeoutReproduced': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', type=Path, required=True)
    parser.add_argument('--rustc', required=True)
    args = parser.parse_args()
    print(json.dumps(run(args.archive, args.rustc), sort_keys=True, separators=(',', ':')))

#!/usr/bin/env python3
"""Explicit finite synthetic GUI fixtures; no agent, signals or desktop capture."""
import ctypes
import json
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
LIB = ctypes.CDLL('/usr/lib/libSystem.B.dylib')
LIB.mach_continuous_time.restype = ctypes.c_uint64

class Timebase(ctypes.Structure):
    _fields_ = [('numer', ctypes.c_uint32), ('denom', ctypes.c_uint32)]

BASE = Timebase()
if LIB.mach_timebase_info(ctypes.byref(BASE)) != 0 or not BASE.denom:
    raise RuntimeError('clock unavailable')

def frame(kind):
    value = dict(version=1, source_continuous_ms=LIB.mach_continuous_time()*BASE.numer//BASE.denom//1000000,
                 seq=1, phase='closed', reason='deadline', grant_remaining_ms=0,
                 starting_workers=0, running_workers=0, stopping_workers=0,
                 cleanup='confirmed', terminal_cleanup_unknown=False, terminal=True)
    if kind == 'unknown':
        value.update(phase='failed', reason='terminal_cleanup_unknown', cleanup='unknown', terminal_cleanup_unknown=True)
    if kind == 'nonterminal':
        value.update(phase='connected', reason='none', cleanup='pending', terminal=False, running_workers=1, grant_remaining_ms=10000)
    if kind == 'invalid_enum':
        value['phase'] = 'PRIVATE_SENTINEL_NEVER_OUTPUT'
    return json.dumps(value).encode()+b'\n'

def run(kind, expected, fault, end='eof', default=False):
    flag = '--check-own-window-eof' if default else '--pilot-exit-on-stream-end'
    child = subprocess.Popen([str(ROOT/'.build/debug/TetherStatus'), flag], cwd=ROOT,
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if kind != 'empty':
        data = b'{malformed}\n' if kind == 'malformed' else frame(kind)
        if kind == 'truncated':
            data = data[:-1]
        child.stdin.write(data)
        child.stdin.flush()
    # Valid terminal alone must leave the viewer alive until actual EOF.
    if kind in ('confirmed', 'stale'):
        time.sleep(3.3 if kind == 'stale' else 0.7)
        if child.poll() is not None:
            raise AssertionError('terminal frame caused premature exit')
    child.stdin.close()
    child.stdin = None
    try:
        stdout, stderr = child.communicate(timeout=12)
    except subprocess.TimeoutExpired:
        print(json.dumps(dict(result='timeout_child_unconfirmed', owned_pid=child.pid)))
        raise SystemExit(1)  # Do not signal or claim cleanup.
    if default:
        assert child.returncode == 0 and stdout == b'OWN_BUNDLE_WINDOW_PASS\n'
    else:
        assert len(stdout) < 2048 and stdout.count(b'\n') == 1
        summary = json.loads(stdout)
        assert summary == dict(version=1, stream_end=end, result='confirmed_terminal' if expected == 0 else 'unknown',
                               fault=fault, reader='joined', exit_code=expected), summary
        assert child.returncode == expected
        assert b'PRIVATE_SENTINEL' not in stdout
    print(json.dumps(dict(case=kind+('_default' if default else ''), result='passed', exit_code=child.returncode)))

if __name__ == '__main__':
    run('confirmed', 0, 'eof')
    run('stale', 2, 'eof')
    run('empty', 2, 'eof')
    run('nonterminal', 2, 'eof')
    run('unknown', 2, 'eof')
    run('malformed', 2, 'malformed', 'rejected')
    run('invalid_enum', 2, 'malformed', 'rejected')
    run('truncated', 2, 'malformed')
    run('empty', 0, 'eof', default=True)
    print('PILOT_LIFECYCLE_CHECKS 9 FAILURES 0')

#!/usr/bin/env python3
"""Explicit finite synthetic GUI fixtures; no agent, signals or desktop capture."""
import ctypes
import fcntl
import json
import os
from pathlib import Path
import subprocess
import tempfile
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

def output_pipe():
    """Caller-owned, dedicated summary pipe; no inherited fd flags are changed."""
    read_fd, write_fd = os.pipe()
    try:
        os.set_blocking(write_fd, False)
        fcntl.fcntl(write_fd, 73, 1)  # Darwin F_SETNOSIGPIPE (SDK sys/fcntl.h).
        return read_fd, write_fd
    except OSError:
        os.close(read_fd)
        os.close(write_fd)
        raise


def run(kind, expected, fault, end='eof', default=False):
    flag = '--check-own-window-eof' if default else '--pilot-exit-on-stream-end'
    out_read, out_write = output_pipe()
    try:
        child = subprocess.Popen([str(ROOT/'.build/debug/TetherStatus'), flag], cwd=ROOT,
                                 stdin=subprocess.PIPE, stdout=out_write, stderr=subprocess.PIPE)
    except OSError:
        os.close(out_read)
        raise
    finally:
        os.close(out_write)
    child.stdout = os.fdopen(out_read, 'rb')
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

def run_output_fault(kind):
    """Wait before draining stdout: communicate() would hide backpressure."""
    read_fd, write_fd = output_pipe()
    alias = os.dup(write_fd)
    capacity = 0
    if kind.startswith('full-'):
        while True:
            try:
                capacity += os.write(write_fd, b'x'*512)
            except BlockingIOError:
                break
    if kind in ('blocking', 'full-blocking'):
        # Deliberately invalid caller; only this newly created pipe is changed.
        os.set_blocking(write_fd, True)
    if kind == 'reader-loss':
        os.close(read_fd)
        read_fd = None
    mask = os.O_ACCMODE | os.O_NONBLOCK | os.O_APPEND | os.O_ASYNC | os.O_SYNC | os.O_DSYNC
    before = (fcntl.fcntl(alias, fcntl.F_GETFL) & mask,
              fcntl.fcntl(alias, 74), fcntl.fcntl(alias, fcntl.F_GETFD))
    with tempfile.TemporaryFile() as errors:
        try:
            child = subprocess.Popen([str(ROOT/'.build/debug/TetherStatus'), '--pilot-exit-on-stream-end'],
                                     cwd=ROOT, stdin=subprocess.PIPE, stdout=write_fd, stderr=errors)
        except OSError:
            os.close(alias)
            if read_fd is not None:
                os.close(read_fd)
            raise
        finally:
            os.close(write_fd)
        input_ok = True
        try:
            child.stdin.write(frame('confirmed'))
            child.stdin.close()
        except BrokenPipeError:
            input_ok = False
        timed_out = False
        try:
            child.wait(timeout=12)
        except subprocess.TimeoutExpired:
            timed_out = True
        # Recover a RED writer only by draining our own pipe, then normal wait.
        received = bytearray()
        deadline = time.monotonic()+3
        if read_fd is not None:
            os.set_blocking(read_fd, False)
            while True:
                try:
                    received.extend(os.read(read_fd, 65536))
                except BlockingIOError:
                    pass
                if child.poll() is not None:
                    break
                if time.monotonic() >= deadline:
                    print(json.dumps(dict(case=kind, result='timeout_child_unconfirmed',
                                          owned_pid=child.pid, reaped=False)), flush=True)
                    raise SystemExit(1)
                time.sleep(.001)
            while True:
                try:
                    received.extend(os.read(read_fd, 65536))
                except BlockingIOError:
                    break
            os.close(read_fd)
        try:
            child.wait(timeout=1)
        except subprocess.TimeoutExpired:
            print(json.dumps(dict(case=kind, result='timeout_child_unconfirmed',
                                  owned_pid=child.pid, reaped=False)), flush=True)
            raise SystemExit(1)
        after = (fcntl.fcntl(alias, fcntl.F_GETFL) & mask,
                 fcntl.fcntl(alias, 74), fcntl.fcntl(alias, fcntl.F_GETFD))
        os.close(alias)
        errors.seek(0, os.SEEK_END)
        error_bytes = errors.tell()
    print(json.dumps(dict(case=kind, reaped=True, timed_out=timed_out,
                          exit_code=child.returncode, capacity_bytes=capacity,
                          alias_flags_unchanged=before == after)))
    assert input_ok and not timed_out, 'viewer failed bounded EOF termination'
    assert child.returncode == 2 and received == b'x'*capacity
    assert before == after and error_bytes <= 1024*1024


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
    for kind in ('full-nonblocking', 'full-blocking', 'reader-loss', 'blocking'):
        run_output_fault(kind)
    print('PILOT_LIFECYCLE_CHECKS 13 FAILURES 0')

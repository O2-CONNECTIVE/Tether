#!/usr/bin/env python3
"""Headless native output boundary; owned pipes/children only, never AppKit."""
import fcntl
import json
import os
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from check_pilot_lifecycle import frame

ROOT = Path(__file__).resolve().parents[1]
# Darwin SDK sys/fcntl.h; Python does not expose these constants.
F_SETNOSIGPIPE, F_GETNOSIGPIPE = 73, 74


def flags(fd):
    # Compare public status flags; Darwin adds kernel FWASWRITTEN on write.
    status_mask = os.O_ACCMODE | os.O_NONBLOCK | os.O_APPEND | os.O_ASYNC | os.O_SYNC | os.O_DSYNC
    return (fcntl.fcntl(fd, fcntl.F_GETFL) & status_mask, fcntl.fcntl(fd, fcntl.F_GETFD),
            fcntl.fcntl(fd, F_GETNOSIGPIPE))


def build(directory):
    source = (ROOT/'Sources/TetherStatus/main.swift').read_text()
    start = source.index('            let bytes = Array(summary.jsonLine.utf8)') if '            let bytes = Array(summary.jsonLine.utf8)' in source else source.index('            exit(PilotOutput.write(summary))')
    block = source[start:source.index('\n        }', start)]
    main = directory/'main.swift'
    main.write_text('''import Foundation
import Darwin
let reader = PipeReader()
reader.startStdin()
let deadline = Date().addingTimeInterval(2)
while reader.pilotSummary() == nil && Date() < deadline { Thread.sleep(forTimeInterval:0.001) }
guard let summary = reader.pilotSummary(), reader.stopAndJoin() else { exit(70) }
if CommandLine.arguments.last == "closed" { _ = Darwin.close(STDOUT_FILENO) }
let marker = Array("INPUT_EOF_READER_JOINED\\n".utf8)
_ = marker.withUnsafeBytes { Darwin.write(STDERR_FILENO,$0.baseAddress,marker.count) }
''' + block + '\n')
    binary = directory/'native-output'
    subprocess.run(['xcrun', 'swiftc', '-module-cache-path', str(directory/'module-cache'),
                    *map(str, sorted((ROOT/'Sources/StatusCore').glob('*.swift'))),
                    str(main), '-o', str(binary)], check=True)
    return binary


def run(binary, kind, expected):
    r, w = os.pipe()
    os.set_blocking(r, False)
    fcntl.fcntl(w, F_SETNOSIGPIPE, 1)
    os.set_blocking(w, False)
    capacity = 0
    if kind.startswith('full'):
        while True:
            try:
                capacity += os.write(w, b'x'*512)
            except BlockingIOError:
                break
    if kind in ('blocking', 'full-blocking'):
        os.set_blocking(w, True)
    if kind == 'no-nosigpipe':
        fcntl.fcntl(w, F_SETNOSIGPIPE, 0)
    sink = w
    extra = None
    if kind == 'reader-loss':
        os.close(r)
        r = None
    elif kind == 'read-only':
        sink = r
    elif kind == 'file':
        extra = tempfile.TemporaryFile()
        sink = extra.fileno()
        os.set_blocking(sink, False)
        fcntl.fcntl(sink, F_SETNOSIGPIPE, 1)
    elif kind == 'invalid-sink':
        extra = open(os.devnull, 'wb')
        sink = extra.fileno()
        os.set_blocking(sink, False)
        fcntl.fcntl(sink, F_SETNOSIGPIPE, 1)
    alias = os.dup(sink)
    before = flags(alias)
    child = subprocess.Popen([str(binary), kind], stdin=subprocess.PIPE, stdout=sink,
                             stderr=subprocess.PIPE)
    child.stdin.write(frame('confirmed'))
    child.stdin.close()
    ready = select.select([child.stderr], [], [], 3)[0]
    marker = child.stderr.readline() if ready else b''
    blocked = False
    try:
        child.wait(timeout=.7)
    except subprocess.TimeoutExpired:
        blocked = True
    # Drain the owned pipe even after RED, then reap normally. No kill fallback.
    data = bytearray()
    if r is not None:
        os.set_blocking(r, False)
        deadline = time.monotonic()+3
        while True:
            try:
                chunk = os.read(r, 65536)
                data.extend(chunk)
            except BlockingIOError:
                pass
            if child.poll() is not None:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError(f'owned child unreaped: {child.pid}')
            time.sleep(.001)
        while True:
            try:
                data.extend(os.read(r, 65536))
            except BlockingIOError:
                break
    child.wait(timeout=1)
    after = flags(alias)
    os.close(alias)
    os.close(w)
    if r is not None:
        os.close(r)
    if extra is not None:
        extra.close()
    child.stderr.close()
    summary = bytes(data[capacity:])
    print(json.dumps(dict(case=kind, capacity_bytes=capacity, blocked_after_join=blocked,
                          exit_code=child.returncode, reaped=True,
                          status_flags_unchanged=before[0] == after[0],
                          descriptor_flags_unchanged=before[1] == after[1],
                          nosigpipe_unchanged=before[2] == after[2])))
    assert marker == b'INPUT_EOF_READER_JOINED\n'
    assert not blocked, 'stdout backpressure prevented bounded termination after reader join'
    assert child.returncode == expected
    assert before == after, (before, after)
    if expected == 0:
        assert json.loads(summary) == dict(version=1, stream_end='eof', result='confirmed_terminal',
                                          fault='eof', reader='joined', exit_code=0)
        assert summary.count(b'\n') == 1
    else:
        assert summary == b''


def main():
    with tempfile.TemporaryDirectory(prefix='tether-output-check-') as temp:
        binary = build(Path(temp))
        for kind in ('full-blocking', 'full-nonblocking', 'blocking', 'reader-loss',
                     'read-only', 'file', 'invalid-sink', 'closed'):
            run(binary, kind, 2)
        run(binary, 'normal', 0)
        checks = Path(temp)/'syscall-checks'
        subprocess.run(['xcrun', 'swiftc', '-module-cache-path', str(Path(temp)/'module-cache'),
                        *map(str, sorted((ROOT/'Sources/StatusCore').glob('*.swift'))),
                        str(ROOT/'Tests/PilotOutputChecks.swift'), '-o', str(checks)], check=True)
        subprocess.run([str(checks)], check=True)
    print('PILOT_OUTPUT_CHECKS 9 FAILURES 0')


if __name__ == '__main__':
    main()

#!/usr/bin/env python3
"""Disposable-runner ONLY. Two roles keep ownership separate from CI verdict.

keeper runs in the background and owns exactly one original test process/log.
observer reports the durable outcome without signalling or replacing owners.
Do not execute before independent final fixture review.
"""
import argparse
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import time

p = argparse.ArgumentParser()
p.add_argument('role', choices=['keeper', 'observer'])
p.add_argument('--report', type=Path, required=True)
p.add_argument('--invocation', required=True)
p.add_argument('--test', type=Path)
p.add_argument('--test-name', choices=['TestCIActualProducerConnection', 'TestCIProducerTrustTLSNegative'], default='TestCIActualProducerConnection')
p.add_argument('--log', type=Path)
a = p.parse_args()
if (os.environ.get('CI_ACTUAL_PRODUCER'), os.environ.get('RUNNER_ENVIRONMENT'),
    os.environ.get('RUNNER_OS'), os.environ.get('RUNNER_ARCH')) != ('1', 'github-hosted', 'macOS', 'ARM64'):
    raise SystemExit('Refused: explicit disposable macOS ARM64 CI only')
if not a.report.is_absolute():
    raise SystemExit('Absolute result path required')
if not re.fullmatch(r'[a-f0-9]{32}', a.invocation) or os.environ.get('CI_PRODUCER_INVOCATION') != a.invocation:
    raise SystemExit('Expected fresh shared invocation identity')
final = a.report.with_suffix('.supervisor.json')
verdict = a.report.with_suffix('.verdict.json')

def publish(value):
    with final.open('x') as f:
        value.update(invocation=a.invocation, test=a.test_name)
        json.dump(value, f)
        f.flush()
        os.fsync(f.fileno())

if a.role == 'keeper':
    if os.environ.get('CI_PRODUCER_RESULT') != str(a.report):
        raise SystemExit('Result environment does not match keeper report')
    if a.report.exists() or a.report.is_symlink() or final.exists() or final.is_symlink() or verdict.exists() or verdict.is_symlink():
        raise SystemExit('Refused stale outcome path')
    if not a.test or not a.test.is_absolute() or not a.log:
        raise SystemExit('Exact absolute test binary and log required')
    # No timeout, process group, signal, CommandContext or cleanup retry.
    # On unknown the original test retains producer/gateway and this keeper
    # retains the original test handle/log until disposable runner destruction.
    with a.log.open('xb') as log:
        child = subprocess.Popen([str(a.test), '-test.run=^' + a.test_name + '$',
                                  '-test.timeout=0', '-test.v=test2json'], stdout=log, stderr=log,
                                 env=os.environ.copy())
        code = child.wait()
        log.flush()
        os.fsync(log.fileno())
    try:
        outcome = json.loads(a.report.read_text())
    except (OSError, ValueError):
        outcome = {}
    passed = code == 0 and outcome.get('outcome') == 'passed' and outcome.get('cleanup') == 'confirmed' and outcome.get('test') == a.test_name and outcome.get('invocation') == a.invocation
    publish({'outcome': 'passed' if passed else 'failed-unknown', 'original_test_exit': code,
             'cleanup': 'confirmed' if passed else 'unconfirmed'})
    sys.exit(0 if passed else 1)

# Observer never owns or signals the test/producer. Failure marker or observation
# timeout fails CI, leaving all original owners to the keeper and test process.
if verdict.exists() or verdict.is_symlink():
    raise SystemExit('Immutable observer verdict already exists')

def decide(passed, reason):
    value = {'outcome': 'passed' if passed else 'failed-unknown',
             'cleanup': 'confirmed' if passed else 'unconfirmed',
             'invocation': a.invocation, 'test': a.test_name, 'reason': reason}
    with verdict.open('x') as f:
        json.dump(value, f)
        f.flush()
        os.fsync(f.fileno())
    if passed:
        print('Exact test process joined after confirmed fixture cleanup')
        sys.exit(0)
    raise SystemExit(reason)

budget = 900 if a.test_name == 'TestCIActualProducerConnection' else 180
deadline = time.monotonic() + budget
while time.monotonic() < deadline:
    for path in (a.report, final):
        try:
            result = json.loads(path.read_text())
        except (OSError, ValueError):
            continue
        if result.get('invocation') != a.invocation or result.get('test') != a.test_name:
            continue
        if result.get('outcome') == 'failed-unknown':
            decide(False, 'Failed/unknown: owners retained; runner teardown is not cleanup proof')
        if path == final and result.get('outcome') == 'passed' and result.get('original_test_exit') == 0 and result.get('cleanup') == 'confirmed':
            decide(True, 'original-test-joined')
    time.sleep(.1)
decide(False, 'Observation timed out: failure/unknown, no cleanup or kill attempted')

"""Independently bind causal evidence to the immutable original failed verdict.

The dedicated CI job and reviewed producer establish origin/order. File digests
are not authentication against a hostile same-user process. No clock joins.
"""
import hashlib
import importlib.util
import json
import os
from pathlib import Path


def module(root, name):
    spec = importlib.util.spec_from_file_location(name, root / 'scripts' / (name + '.py'))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def verify(root, temp, bind, original):
    failure = module(root, 'post_verdict_failure')
    record = module(root, 'wry_post_verdict_record')
    trace = temp / 'tools/app-startup'
    raw_manifest = failure.read(trace / 'post-verdict.manifest.json', 2048)
    manifest = failure.manifest(raw_manifest)
    if raw_manifest != failure.read(temp / 'post-verdict.manifest.json', 2048):
        raise ValueError('MANIFEST_REJECTED')
    if any(manifest[k] != v for k, v in bind.items()): raise ValueError('IDENTITY_REJECTED')
    if manifest['run'] != os.environ.get('GITHUB_RUN_ID') or os.environ.get('GITHUB_RUN_ATTEMPT') != '1':
        raise ValueError('RUN_REJECTED')
    from post_verdict_anchor import expected, digest
    import post_verdict_engine
    engine_hash = post_verdict_engine.admit(temp, bind)
    admitted = {'qualificationSha256': digest(expected(bind)),
                'manifestSha256': hashlib.sha256(raw_manifest).hexdigest(),
                'engineQualificationSha256': engine_hash, **bind}
    if failure.read(temp / 'post-verdict-admitted.json', 2048) != failure.canonical(admitted):
        raise ValueError('ADMISSION_REJECTED')
    files = original['files']
    if (files['launch'] != files['final'] or manifest['specimenSha256'] != files['launch']['data'].get('exeSha256')
            or files['native']['data'].get('nativeTestsRun') != 1
            or files['native']['data'].get('nativeTestsPassed') != 0):
        raise ValueError('ORIGINAL_VERDICT_REJECTED')
    for saved, original_name in (('post-verdict-native.json', 'native-observation.json'),
                                  ('post-verdict-timing.json', 'session-startup-timing.json')):
        if failure.read(trace / saved) != failure.read(temp / 'evidence' / original_name):
            raise ValueError('COPY_REJECTED')
    raw_receipt = failure.read(trace / 'post-verdict-failure.json', 2048)
    if raw_receipt != failure.canonical(failure.receipt(trace)):
        raise ValueError('RECEIPT_REJECTED')
    manifest_hash = hashlib.sha256(raw_manifest).hexdigest()
    failure_hash = hashlib.sha256(raw_receipt).hexdigest()
    signal = {'protocol': 'post-verdict-signal-1', 'manifestSha256': manifest_hash,
              'failureSha256': failure_hash, **bind, 'specimenSha256': manifest['specimenSha256']}
    if failure.read(trace / 'post-verdict.signal', 2048) != failure.canonical(signal):
        raise ValueError('SIGNAL_REJECTED')
    collection = json.loads(failure.read(temp / 'evidence/native-result.json'))
    companion = record.read_record(trace / 'wry-post-verdict-probe.json', collection)
    if companion['delivery'] != files['delivery']['data']:
        raise ValueError('PROJECTION_REJECTED')
    anchor = companion['anchor']
    if anchor['state'] != 'observed' or anchor['manifestSha256'] != manifest_hash or anchor['failureSha256'] != failure_hash:
        raise ValueError('ANCHOR_REJECTED')
    # Expected proxy cancellation is allowed. Contrary headers/success invalidate
    # this pre-header endpoint rather than turning a late HTTP result into success.
    driver = files['driver']['data']
    if driver.get('requestStage') != 'dropped-before-headers':
        raise ValueError('DRIVER_ENDPOINT_UNCONFIRMED')
    return {'state': 'verified', 'manifestSha256': manifest_hash, 'failureSha256': failure_hash,
            'claim': ('controller-pre-send-observation-after-verdict' if record.send_after_verdict(companion)
                      else 'unknown'), 'record': companion}


def companion(root, temp, bind, original, original_digest):
    try:
        evidence = verify(root, temp, bind, original)
    except (OSError, ValueError, TypeError, KeyError, ImportError, RecursionError):
        evidence = {'state': 'unavailable-or-rejected', 'claim': 'unknown'}
    return {'schema': 1, 'variant': 'post-verdict-causal-publication-1', **bind,
            'originalRecordSha256': original_digest, 'evidence': evidence}

"""Closed companion causal record. Positive is send-after-verdict, never callback-entry timing.

The delivery subrecord preserves the original six-sample ordinal schema by removing
only the disclosed anchor reservation. The causal seven-position sequence is here.
No clock join, missing-event stall claim, or Windows query invention is used.
"""
import json
from pathlib import Path
import re
import stat
import wry_delivery_record as old

TOP = {'schema', 'variant', 'purpose', 'anchor', 'samples', 'delivery'}
ANCHOR = {'state', 'seq', 'elapsedUs', 'manifestSha256', 'failureSha256'}
MAX_BYTES = 12288


def validate(value):
    if (type(value) is not dict or set(value) != TOP or type(value['schema']) is not int
            or value['schema'] != 1 or value['variant'] != 'wry-sdk-post-verdict-1'
            or value['purpose'] != 'diagnostic-hints-only'):
        raise ValueError('POST_VERDICT_RECORD_REJECTED')
    delivery = old.validate(value['delivery'])
    anchor = value['anchor']
    if (type(anchor) is not dict or set(anchor) != ANCHOR
            or not old.enum(anchor['state'], {'missing', 'interrupted', 'observed'})
            or (anchor['seq'] is not None and (not old.integer(anchor['seq'], 7) or anchor['seq'] == 0))):
        raise ValueError('POST_VERDICT_RECORD_REJECTED')
    if anchor['state'] == 'observed':
        if (anchor['seq'] is None or not old.integer(anchor['elapsedUs'])
                or any(type(anchor[k]) is not str or not re.fullmatch('[0-9a-f]{64}', anchor[k])
                       for k in ('manifestSha256', 'failureSha256'))):
            raise ValueError('POST_VERDICT_RECORD_REJECTED')
    elif (any(anchor[k] is not None for k in ('elapsedUs', 'manifestSha256', 'failureSha256'))
          or (anchor['state'] == 'missing' and anchor['seq'] is not None)):
        raise ValueError('POST_VERDICT_RECORD_REJECTED')
    if type(value['samples']) is not list or len(value['samples']) != 6:
        raise ValueError('POST_VERDICT_RECORD_REJECTED')
    for i, (sample, original) in enumerate(zip(value['samples'], delivery['samples'])):
        if (type(sample) is not dict or set(sample) != {'seq', 'anchorSeqObserved'}
                or not old.integer(sample['anchorSeqObserved'], 7)
                or (i != 3 and sample['anchorSeqObserved'] != 0)):
            raise ValueError('POST_VERDICT_RECORD_REJECTED')
        seq = sample['seq']
        if original['state'] == 'observed':
            if not old.integer(seq, 7) or seq == 0:
                raise ValueError('POST_VERDICT_RECORD_REJECTED')
            projected = seq - int(anchor['seq'] is not None and seq > anchor['seq'])
            if projected != original['seq']:
                raise ValueError('POST_VERDICT_PROJECTION_REJECTED')
        elif seq is not None or sample['anchorSeqObserved'] != 0:
            raise ValueError('POST_VERDICT_RECORD_REJECTED')
    return value


def send_after_verdict(value):
    """One-sided same-record witness; absent/invalid/lost evidence is finite unknown."""
    validate(value)
    d, a, samples = value['delivery'], value['anchor'], value['samples']
    if (not d['registered'] or not d['sealed'] or d['inFlight'] != 0 or d['lossDetected']
            or d['scopeAttemptsCapped'] != 2 or a['state'] != 'observed'):
        return False
    if any(s['state'] != 'observed' or s['ambiguous'] or s['scopeThreadIsTraceThread'] is not True
           or any(s[k] > 1 for k in old.COUNTS) for s in d['scopes']):
        return False
    rows = [(a['seq'], a['elapsedUs'])]
    for sample, original in zip(samples, d['samples']):
        if original['state'] not in {'observed', 'missing'}:
            return False
        if original['state'] == 'observed':
            if (original['onScopeThread'] is not True or original['phase'] == 'unknown'
                    or original['relation'] == 'unknown'
                    or (original['phase'] in {'get-message', 'dispatch-message'} and original['relation'] != 'original-wait')
                    or (original['phase'] == 'sdk-api' and original['relation'] != 'same-creation')):
                return False
            rows.append((sample['seq'], original['elapsedUs']))
    # All represented reservations must be unique and contiguous. Sorting checks,
    # rather than repairs, the record's sequence/timestamp consistency.
    ordered = sorted(rows)
    if ([seq for seq, _ in ordered] != list(range(1, len(rows) + 1))
            or [time for _, time in ordered] != sorted(time for _, time in ordered)
            or any(time > d['sealElapsedUs'] for _, time in ordered)):
        return False
    enter, sent = d['samples'][3:5]
    if enter['state'] != 'observed' or sent['state'] != 'observed' or sent['sendSucceeded'] is not True:
        return False
    # No requirement for an optional later guard-drop to establish a prior send.
    for start in (0, 3):
        seen = [samples[i]['seq'] for i in range(start, start + 3) if samples[i]['seq'] is not None]
        if seen != sorted(seen): return False
    return samples[3]['anchorSeqObserved'] == a['seq'] < samples[3]['seq'] < samples[4]['seq']


def read_record(path: Path, collection):
    info = path.lstat()
    if (type(collection) is not dict or collection.get('appStartupFileClosed') is not True
            or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
            or getattr(info, 'st_file_attributes', 0) & 1024 or info.st_size > MAX_BYTES):
        raise ValueError('POST_VERDICT_COLLECTION_REJECTED')
    data = path.read_bytes()
    if len(data) > MAX_BYTES: raise ValueError('POST_VERDICT_COLLECTION_REJECTED')
    try:
        text = data.decode('utf-8'); value = json.loads(text)
        if json.dumps(value, separators=(',', ':')) != text:
            raise ValueError('POST_VERDICT_COLLECTION_REJECTED')
    except (UnicodeError, RecursionError):
        raise ValueError('POST_VERDICT_COLLECTION_REJECTED') from None
    return validate(value)

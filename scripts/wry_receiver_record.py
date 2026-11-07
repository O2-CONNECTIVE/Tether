"""Closed finite poll summary. Empty is an instant observation, never continued liveness."""
import json
import stat
COUNTS = ('readyCapped', 'emptyCapped', 'disconnectedCapped')
def validate(v):
    if (type(v) is not dict or set(v) != {'schema','variant','purpose','registered','sealed','lossDetected','buckets'}
        or type(v['schema']) is not int or v['schema'] != 1 or v['variant'] != 'wry-receiver-polls-1'
        or v['purpose'] != 'diagnostic-hints-only'
        or any(type(v[k]) is not bool for k in ('registered','sealed','lossDetected'))
        or type(v['buckets']) is not list or len(v['buckets']) != 3): raise ValueError('RECEIVER_RECORD_REJECTED')
    for i,r in enumerate(v['buckets']):
        if (type(r) is not dict or set(r) != {'bucket','relation',*COUNTS}
            or r['bucket'] != ('environment','controller','unbound')[i]
            or r['relation'] not in ('original-wait','unknown')
            or any(type(r[k]) is not int or not 0 <= r[k] <= 2 for k in COUNTS)):
            raise ValueError('RECEIVER_RECORD_REJECTED')
        if r['relation'] == 'original-wait' and (i == 2 or not any(r[k] for k in COUNTS)
            or not v['registered'] or not v['sealed'] or v['lossDetected']): raise ValueError('RECEIVER_RECORD_REJECTED')
    return v

def complete(value):
    value = validate(value)
    return (value['registered'] and value['sealed'] and not value['lossDetected']
            and all(row['relation'] == 'original-wait' and any(row[k] for k in COUNTS)
                    for row in value['buckets'][:2]))

def read_record(path, collection):
    s=path.lstat()
    if (type(collection) is not dict or collection.get('appStartupFileClosed') is not True or not stat.S_ISREG(s.st_mode)
        or s.st_nlink != 1 or getattr(s,'st_file_attributes',0)&1024 or s.st_size>2048): raise ValueError('RECEIVER_COLLECTION_REJECTED')
    try:
        text=path.read_bytes().decode('utf-8'); v=json.loads(text)
    except (UnicodeError, RecursionError):
        raise ValueError('RECEIVER_COLLECTION_REJECTED') from None
    if len(text)>2048 or json.dumps(v,separators=(',',':')) != text: raise ValueError('RECEIVER_COLLECTION_REJECTED')
    return validate(v)

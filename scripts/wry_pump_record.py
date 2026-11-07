"""Closed last-published pump brackets, never sampled PCs or a causal diagnosis."""
import json
import stat

COUNTS = ('scopeEntriesCapped', 'waitEntriesCapped', 'waitDropsCapped', 'scopeDropsCapped',
          'getEntriesCapped', 'getErrorReturnsCapped', 'getQuitReturnsCapped',
          'getOtherReturnsCapped', 'dispatchEntriesCapped', 'dispatchReturnsCapped')
STATES = ('missing', 'between-calls', 'get-message-bracket', 'dispatch-message-bracket',
          'wait-ended', 'unknown')


def validate(value):
    if (type(value) is not dict or set(value) != {
            'schema', 'variant', 'purpose', 'registered', 'sealRequested', 'sealed', 'lossDetected', 'slots'}
            or type(value['schema']) is not int or value['schema'] != 1
            or value['variant'] != 'wry-pump-brackets-1' or value['purpose'] != 'diagnostic-hints-only'
            or any(type(value[k]) is not bool for k in ('registered', 'sealRequested', 'sealed', 'lossDetected'))
            or type(value['slots']) is not list or len(value['slots']) != 2):
        raise ValueError('PUMP_RECORD_REJECTED')
    coherent = value['registered'] and value['sealRequested'] and value['sealed'] and not value['lossDetected']
    for name, row in zip(('environment', 'controller'), value['slots']):
        if (type(row) is not dict or set(row) != {'slot', 'relation', 'state', *COUNTS}
                or row['slot'] != name or row['relation'] not in ('original-wait', 'unknown')
                or row['state'] not in STATES
                or any(type(row[k]) is not int or not 0 <= row[k] <= 2 for k in COUNTS)):
            raise ValueError('PUMP_RECORD_REJECTED')
        if not coherent and (row['state'] != 'unknown' or row['relation'] != 'unknown'):
            raise ValueError('PUMP_RECORD_REJECTED')
        if row['state'] in ('missing', 'unknown'):
            if row['relation'] != 'unknown': raise ValueError('PUMP_RECORD_REJECTED')
        elif (row['relation'] != 'original-wait' or row['scopeEntriesCapped'] != 1
              or row['waitEntriesCapped'] != 1):
            raise ValueError('PUMP_RECORD_REJECTED')
        if row['relation'] == 'original-wait':
            # These are necessary evidence checks, never subtraction of capped history.
            if (row['waitDropsCapped'] != int(row['state'] == 'wait-ended')
                    or row['scopeDropsCapped'] > 1
                    or (row['scopeDropsCapped'] and row['state'] != 'wait-ended')
                    or (row['state'] == 'get-message-bracket' and not row['getEntriesCapped'])
                    or (row['state'] == 'dispatch-message-bracket' and
                        (not row['dispatchEntriesCapped'] or not row['getOtherReturnsCapped']))):
                raise ValueError('PUMP_RECORD_REJECTED')
    return value


def complete(value):
    value = validate(value)
    return (value['registered'] and value['sealRequested'] and value['sealed'] and not value['lossDetected']
            and all(row['relation'] == 'original-wait' and row['state'] not in ('missing', 'unknown')
                    for row in value['slots']))


def read_record(path, collection):
    info = path.lstat()
    if (type(collection) is not dict or collection.get('appStartupFileClosed') is not True
            or not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
            or getattr(info, 'st_file_attributes', 0) & 1024 or info.st_size > 4096):
        raise ValueError('PUMP_COLLECTION_REJECTED')
    try:
        text = path.read_bytes().decode('utf-8')
        value = json.loads(text)
    except (UnicodeError, RecursionError):
        raise ValueError('PUMP_COLLECTION_REJECTED') from None
    if len(text) > 4096 or json.dumps(value, separators=(',', ':')) != text:
        raise ValueError('PUMP_COLLECTION_REJECTED')
    return validate(value)

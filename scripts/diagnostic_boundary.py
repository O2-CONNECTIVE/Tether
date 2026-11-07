"""Pure synthetic-record boundary prototype; not wired into runtime or CI output."""
import json
from pathlib import Path

MAX_BYTES = 16384
_ENUMS = json.loads((Path(__file__).resolve().parents[1] / 'ci-producer/DIAGNOSTIC_ENUMS.json').read_text())
GROUPS = (_ENUMS['result_fields'], _ENUMS['native_fields'])
ALLOWED = {'stage': _ENUMS['stages'], 'failure_code': _ENUMS['failure_codes'], **GROUPS[0], **GROUPS[1]}


def _reject(reason):
    return {'validation': 'unavailable', 'reason': reason, 'fields': {}}


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate-field')
        result[key] = value
    return result


def _invalid_constant(_value):
    raise ValueError('invalid-constant')


def validate_record(raw):
    """Validate bounded JSON bytes containing ONLY the diagnostic projection.

    Not a raw-log scanner, full producer marker validator, verdict, or authority
    check. Rejection returns fixed reasons only, never input or exception text.
    """
    if type(raw) is not bytes:
        return _reject('invalid-record')
    if len(raw) > MAX_BYTES:
        return _reject('oversized-record')
    try:
        record = json.loads(raw.decode('utf-8'), object_pairs_hook=_unique_object,
                            parse_constant=_invalid_constant)
    except (ValueError, RecursionError):
        return _reject('invalid-record')
    if type(record) is not dict:
        return _reject('invalid-record')
    if set(record) - ALLOWED.keys():
        return _reject('unexpected-field')
    if not set(_ENUMS['fields']) <= record.keys():
        return _reject('incomplete-record')
    for group in GROUPS:
        if record.keys() & group.keys() and not group.keys() <= record.keys():
            return _reject('incomplete-record')
    for field, value in record.items():
        if type(value) is not str or value not in ALLOWED[field]:
            return _reject('invalid-enum')
    # Copy canonical strings rather than any observed source bytes.
    fields = {field: next(item for item in ALLOWED[field] if item == value)
              for field, value in record.items()}
    for field in _ENUMS['native_fields']:
        if field not in fields or field == 'native_capture':
            continue
        if fields.get('native_capture') != 'matched':
            fields[field] = 'unavailable'
            continue
        for prefix in ('native_first_diag_', 'native_final_diag_'):
            if (field.startswith(prefix) and field != prefix + 'available'
                    and record.get(prefix + 'available') != 'yes'):
                fields[field] = 'unavailable'
    return {'validation': 'record-valid', 'reason': 'allowlisted-record', 'fields': fields}

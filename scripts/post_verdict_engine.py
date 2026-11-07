"""Closed same-job Windows 7.6.9 engine-stop qualification, not runner propagation."""
import hashlib
import json
import stat

CASES = ('before-entry', 'during-grace', 'admission-overbudget',
         'diagnostic-fault', 'finish-fault', 'cleanup-fault')
TOP = {'childExitCode', 'childState', 'controlCommit', 'engineVersion', 'protocol',
       'results', 'sourceCommit', 'sourceTree'}
ROW = {'case', 'cleanupCount', 'engineVersion', 'environmentRestored', 'finishCount',
       'nativeTestsPassed', 'nativeTestsRun', 'normalCloseVerified', 'passed',
       'protocol', 'state', 'stopElapsedMs', 'stopState'}


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode('ascii')


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def require(ok):
    if not ok: raise ValueError('ENGINE_QUALIFICATION_REJECTED')


def validate(value, bind):
    require(type(value) is dict and set(value) == TOP)
    require(type(value['childExitCode']) is int and value['childExitCode'] == 0
            and value['childState'] == 'exited' and value['engineVersion'] == '7.6.9'
            and value['protocol'] == 'post-verdict-engine-qualification-1'
            and all(value[k] == v for k, v in bind.items())
            and type(value['results']) is list and len(value['results']) == len(CASES))
    for index, (case, row) in enumerate(zip(CASES, value['results'])):
        require(type(row) is dict and set(row) == ROW)
        require(row['case'] == case and row['protocol'] == 'post-verdict-engine-stop-1'
                and row['engineVersion'] == '7.6.9' and row['environmentRestored'] is True
                and row['normalCloseVerified'] is False and row['passed'] is True)
        for key, count in (('cleanupCount', 1), ('finishCount', 1), ('nativeTestsPassed', 0), ('nativeTestsRun', 1)):
            require(type(row[key]) is int and row[key] == count)
        require(row['state'] == ('Stopped' if index < 2 else 'Completed' if index == 2 else 'Failed')
                and row['stopState'] == ('completed' if index < 2 else 'not-requested')
                and type(row['stopElapsedMs']) is int
                and 0 <= row['stopElapsedMs'] <= (2999 if index < 2 else 15000 if index == 2 else 5000))
    return value


def load(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1
            and not getattr(info, 'st_file_attributes', 0) & 1024 and 0 < info.st_size <= 8192)
    with path.open('rb') as stream: raw = stream.read(8193)
    require(len(raw) == info.st_size)
    try: value = json.loads(raw)
    except (RecursionError, UnicodeError):
        raise ValueError('ENGINE_QUALIFICATION_REJECTED') from None
    require(raw == canonical(value))
    return value


def receipt(temp, bind):
    return validate(load(temp / 'post-verdict-engine-qualified.json'), bind)


def published(value, bind):
    return {'engineQualificationSha256': digest(value), **bind}


def publish(temp, bind):
    value = receipt(temp, bind)
    print('WRY_POST_VERDICT_ENGINE_QUALIFICATION_BEGIN')
    print(canonical(value).decode())
    print('WRY_POST_VERDICT_ENGINE_QUALIFICATION_SHA256=' + digest(value))
    print('WRY_POST_VERDICT_ENGINE_QUALIFICATION_END', flush=True)
    with (temp / 'post-verdict-engine-published.json').open('xb') as stream:
        stream.write(canonical(published(value, bind)))


def admit(temp, bind):
    value = receipt(temp, bind)
    require(canonical(load(temp / 'post-verdict-engine-published.json')) == canonical(published(value, bind)))
    return digest(value)

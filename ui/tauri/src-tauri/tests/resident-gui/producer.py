"""Fixed synthetic pipe source. No network, credentials or work execution."""
import ctypes
import json
import select
import sys
import time

def status_frame(state, timestamp):
    state['revision'] += 1  # A refreshed deadline is a new observation.
    return json.dumps(dict(protocolVersion=1, type='status', sourceContinuousMs=timestamp, snapshot=state))


def disposition(request, revision):
    return 'accepted' if request['expectedRevision'] == revision else 'stale'


def main():
    lib = ctypes.CDLL('/usr/lib/libSystem.B.dylib')
    class Timebase(ctypes.Structure):
        _fields_ = [('n', ctypes.c_uint32), ('d', ctypes.c_uint32)]
    tb = Timebase()
    assert lib.mach_timebase_info(ctypes.byref(tb)) == 0 and tb.d
    lib.mach_continuous_time.restype = ctypes.c_uint64

    def now():
        return lib.mach_continuous_time() * tb.n // tb.d // 1000000

    state = dict(protocolVersion=1, source='resident-owner', state='source-observation',
                 revision=0, validForMs=3000, standby='off', registration='disconnected',
                 caller=None, work='idle', cleanup='confirmed', canRequestOn=True,
                 canRequestOff=False, transition=None)

    def status():
        print(status_frame(state, now()), flush=True)

    status()
    last_request = 0
    for intent in ['on', 'off']:
        while True:
            while not select.select([sys.stdin], [], [], .2)[0]:
                status()
            request = json.loads(sys.stdin.readline(4097))['request']
            expected = request['requestId']
            assert isinstance(expected, int) and last_request < expected <= 9007199254740991
            assert request['intent'] == intent
            assert 0 < request['expectedRevision'] <= state['revision']
            last_request = expected
            accepted = disposition(request, state['revision']) == 'accepted'
            print(json.dumps(dict(protocolVersion=1, type='receipt', receipt=dict(
                protocolVersion=1, requestId=expected, intent=intent,
                disposition='accepted' if accepted else 'stale'))), flush=True)
            if not accepted:
                state['transition'] = dict(requestId=expected, intent=intent, outcome='rejected')
                status()  # Same pending identity is explicitly resolved, never accepted.
                continue
            time.sleep(.2)  # acceptance is intentionally observable before application
            state.update(standby=intent,
                         registration='authenticated' if intent == 'on' else 'disconnected',
                         canRequestOn=intent == 'off', canRequestOff=intent == 'on',
                         transition=dict(requestId=expected, intent=intent, outcome='applied'))
            status()
            break
    while True:
        if select.select([sys.stdin], [], [], .2)[0]:
            assert sys.stdin.readline(4097) == ''  # only original owner Close now
            break
        status()
    print(json.dumps(dict(protocolVersion=1, type='closed', sourceContinuousMs=now(), outcome='confirmed')), flush=True)


if __name__ == '__main__':
    main()

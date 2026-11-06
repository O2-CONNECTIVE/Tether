"""Closed diagnostic-only schemas; no acceptance/profile policy replacement."""
import re

CODES={'hook-not-reached','ok','invalid-set','duplicate-identity','slot-mismatch','path-invalid','overrun','hook-failed','unavailable','identity-mismatch','creation-unknown','creation-mismatch','handle-release-uncertain','exited-before','exited-during','argv-missing','argv-ambiguous','argv-unavailable','profile-outside','directory-unavailable','directory-reparse','directory-alias','directory-changed','directory-limit','directory-dispose-uncertain','set-changed','set-unknown','capture-unavailable'}
STATES={'not-observed','live','exited','identity-mismatch','unavailable','creation-unknown','creation-mismatch','handle-release-uncertain'}
DIRECTORIES={'not-observed','not-applicable','ok','directory-unavailable','directory-reparse','directory-alias','directory-changed','directory-limit','directory-dispose-uncertain'}

def need(value):
    if not value: raise ValueError('OWNED_EVIDENCE_REJECTED')


def capture_safe(v):
    need(isinstance(v,dict) and set(v)=={'schema','instrumented','originalGatesChanged','point','budgetMs','elapsedMs','elapsedClamped','overrun','directoryGuarantee','atomicNamespace','childrenExpected','omittedRows','rows','complete','code','setAgreement'})
    need(type(v['schema']) is int and v['schema']==1 and v['instrumented'] is True and v['originalGatesChanged'] is False and v['atomicNamespace'] is False)
    need(v['point']=='owned-before-close' and v['directoryGuarantee']=='per-row-handle-boundaries-nonatomic')
    need(type(v['budgetMs']) is int and v['budgetMs']==2000 and type(v['elapsedMs']) is int and 0<=v['elapsedMs']<=60000)
    need(all(type(v[k]) is bool for k in ('elapsedClamped','overrun','complete')))
    need(v['code'] in CODES and v['setAgreement'] in {'not-observed','unchanged','changed','unknown'})
    for key in ('childrenExpected','omittedRows'):need(v[key] is None or type(v[key]) is int and 0<=v[key]<=4096)
    need(isinstance(v['rows'],list) and len(v['rows'])<=32)
    need(v['childrenExpected'] is None and v['omittedRows'] is None or v['childrenExpected'] is not None and len(v['rows'])<=v['childrenExpected'] and v['omittedRows']==max(0,v['childrenExpected']-32))
    for index,row in enumerate(v['rows'],1):
        need(isinstance(row,dict) and set(row)=={'slot','parentSlot','role','originalHandleVerified','identityBound','before','after','argvStatus','mapping','directoryStatus','code'})
        need(type(row['slot']) is int and row['slot']==index and type(row['parentSlot']) is int and -1<=row['parentSlot']<=4096)
        need(row['role'] in {'webview','console-host','other'} and type(row['originalHandleVerified']) is bool and (row['identityBound'] is None or type(row['identityBound']) is bool))
        need(row['before'] in STATES and row['after'] in STATES and row['argvStatus'] in {'not-observed','not-applicable','unknown','ambiguous','single'})
        need(row['mapping'] in {'unknown','not-applicable','requested-root','exact-runtime-ebwebview-child','outside'} and row['directoryStatus'] in DIRECTORIES and row['code'] in CODES|{'not-observed'})
    if v['complete']:
        need(v['code']=='ok' and not v['overrun'] and not v['elapsedClamped'] and v['elapsedMs']<=2000 and v['setAgreement']=='unchanged')
        need(type(v['childrenExpected']) is int and 1<=v['childrenExpected']<=32 and len(v['rows'])==v['childrenExpected'] and v['omittedRows']==0)
        need(any(r['role']=='webview' for r in v['rows']))
        for row in v['rows']:
            need(row['originalHandleVerified'] and row['identityBound'] is True and row['before']==row['after']=='live' and row['parentSlot']>=0 and row['code']=='ok')
            if row['role']=='webview':need(row['argvStatus']=='single' and row['mapping'] in {'requested-root','exact-runtime-ebwebview-child'} and row['directoryStatus']=='ok')
            else:need(row['argvStatus']==row['mapping']==row['directoryStatus']=='not-applicable')
    return v


def placeholder():
    return capture_safe({'schema':1,'instrumented':True,'originalGatesChanged':False,'point':'owned-before-close','budgetMs':2000,'elapsedMs':0,'elapsedClamped':False,'overrun':False,'directoryGuarantee':'per-row-handle-boundaries-nonatomic','atomicNamespace':False,'childrenExpected':None,'omittedRows':None,'rows':[],'complete':False,'code':'hook-not-reached','setAgreement':'not-observed'})


def verdict_safe(v):
    need(isinstance(v,dict) and set(v)=={'schema','instrumented','captureComplete','oldContinue','scopePassed','originalFailurePreserved','pr200Blocked','status'})
    need(type(v['schema']) is int and v['schema']==1 and v['instrumented'] is True and v['originalFailurePreserved'] is True and v['pr200Blocked'] is True)
    need(all(type(v[k]) is bool for k in ('captureComplete','scopePassed')) and (v['oldContinue'] is None or type(v['oldContinue']) is bool))
    need(v['scopePassed'] is (v['captureComplete'] and v['oldContinue'] is True))
    need(v['status'] in {'observed','not-reached','capture-unavailable','overlay-drift','preflight-failed'})
    return v


def preflight_safe(v):
    need(isinstance(v,dict) and set(v)=={'schema','kind','stages','passed','casesExpected','casesCompleted','failureCase','runtimeVersions'})
    need(type(v['schema']) is int and v['schema']==1 and v['kind']=='owned-hook-preflight' and type(v['passed']) is bool)
    need(type(v['casesExpected']) is int and v['casesExpected']==57 and type(v['casesCompleted']) is int and v['casesCompleted'] in {0,57})
    need(isinstance(v['stages'],dict) and set(v['stages'])=={'parse','fixture'})
    for stage in v['stages'].values():
        need(isinstance(stage,dict) and set(stage)=={'runtimeExecuted','exitCode','passed','code'} and type(stage['passed']) is bool)
        need(stage['code'] in {'not-run','pass','fixture-rejected','command-timeout','command-unavailable','wrapper-uncertain'})
        need(stage['runtimeExecuted'] is None if stage['code']=='wrapper-uncertain' else type(stage['runtimeExecuted']) is bool)
        need(stage['exitCode'] is None or type(stage['exitCode']) is int and -2147483648<=stage['exitCode']<=4294967295)
        need(stage['passed'] is (stage['code']=='pass'))
        need(not stage['passed'] or stage['runtimeExecuted'] and stage['exitCode']==0)
        need(stage['code'] not in {'not-run','command-unavailable'} or not stage['runtimeExecuted'] and stage['exitCode'] is None)
        need(stage['code'] not in {'command-timeout','wrapper-uncertain'} or stage['exitCode'] is None)
        need(stage['code']!='command-timeout' or stage['runtimeExecuted'] is True)
        need(stage['code']!='fixture-rejected' or stage['runtimeExecuted'] is True and type(stage['exitCode']) is int)
    need(v['stages']['parse']['passed'] or v['stages']['fixture']['code']=='not-run')
    need(v['passed'] is all(s['passed'] for s in v['stages'].values()))
    need(v['casesCompleted']==(57 if v['stages']['fixture']['passed'] else 0))
    need(isinstance(v['runtimeVersions'],dict) and set(v['runtimeVersions'])=={'python','powershell'})
    for item in v['runtimeVersions'].values():need(item is None or isinstance(item,str) and re.fullmatch(r'[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,5})?',item))
    need(not v['passed'] or v['runtimeVersions']['python']=='3.13.17' and v['runtimeVersions']['powershell'] is not None)
    known='load root child all-child-slots outside nested-child sibling null-argv empty-argv duplicate-argv split-argv quoted-suffix invalid-path shuffled-slots duplicate-slots missing-handle duplicate-identity duplicate-handle row-limit pid-reuse creation-mismatch creation-unknown exited-before exited-during rights-unknown release-uncertain reparse alias directory-changed directory-partial directory-dispose new-child set-unknown overrun partial throw-contained close-all close-failure close-throw argv-native native-layout missing-child-name missing-child-parent null-child missing-entry-slot missing-entry-identity null-entry time-unknown-sticky set-overrun final-overrun borrow-ok borrow-query-throw borrow-addref-fail borrow-release-fail no-webview hook-throw-preserves-close hook-save-failure-preserves-close hook-success-isolated'.split()
    need(v['failureCase'] in set(known)|{'not-observed','unknown','missing','conflicting','truncated','not-applicable','unclassified-case'})
    need(not v['passed'] or v['failureCase']=='not-applicable')
    return v

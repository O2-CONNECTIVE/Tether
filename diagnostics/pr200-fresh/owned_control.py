"""One opt-in instrumented trial. Does not change the old verdict or retry."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import owned_overlay as overlay
import owned_schema as schema
import phase2
import powershell_preflight
import report


def checkpoint(path,value):
    path.parent.mkdir(parents=True,exist_ok=True)
    pending=path.with_suffix('.tmp');pending.write_text(json.dumps(value,sort_keys=True,separators=(',',':')),encoding='utf-8');pending.replace(path)


def phase2_projection(raw):
    text=raw.decode('utf-8')
    text=text.replace('import owned_schema\nimport owned_overlay\n','')
    text=text.replace('def trial(source, control, root, index, *, supervisor_path=None, harness_path=None):','def trial(source, control, root, index):')
    text=text.replace("str(supervisor_path or native / 'supervise.ps1')", "str(native / 'supervise.ps1')")
    text=text.replace("str(harness_path or native)","str(native)")
    addition=",\n                  'owned-collection.json': owned_schema.capture_safe,\n                  'owned-overlay-identity.json': owned_overlay.manifest_safe,\n                  'owned-capture-verdict.json': owned_schema.verdict_safe,\n                  'owned-hook-preflight.json': owned_schema.preflight_safe"
    schema.need(text.count(addition)==1)
    return text.replace(addition,'').encode('utf-8')


def verify_source_control(source,control):
    report.verify_source(source)
    expected=os.environ.get('EXPECTED_CONTROL','')
    schema.need(re.fullmatch('[0-9a-f]{40}',expected) and overlay.git(control,'rev-parse','HEAD').decode().strip()==expected)
    schema.need(not overlay.git(control,'status','--porcelain','--untracked-files=all').strip())
    contract=control.parent/'diagnostic'
    schema.need(overlay.git(contract,'rev-parse','HEAD').decode().strip()=='613f4ff550cd65334b00b262a0beebbc3e069fad' and overlay.git(contract,'rev-parse','HEAD^{tree}').decode().strip()=='627bdb664be8f3a9b807077466c7473e915387b2')
    schema.need(not overlay.git(contract,'status','--porcelain','--untracked-files=all').strip())
    import verify_diagnostic_source
    for name in verify_diagnostic_source.HELPERS:
        relative='diagnostics/pr200-fresh/'+name
        original=overlay.git(contract,'show','HEAD:'+relative)
        actual=overlay.git(control,'show',expected+':'+relative)
        schema.need(phase2_projection(actual)==original if name=='phase2.py' else actual==original)
    for name in ('owned_control.py','owned_overlay.py','owned_schema.py','owned-capture.ps1','owned-native.cs','owned-capture.test.ps1','parse-owned-sources.ps1','phase2.py'):
        relative='diagnostics/pr200-fresh/'+name;blob=overlay.git(control,'show',expected+':'+relative);actual=(control/relative).read_bytes()
        schema.need(actual in (blob,blob.replace(b'\n',b'\r\n')))
    return True


def fixture_failure(raw):
    if len(raw)>4096:return 'truncated'
    try:lines=raw.decode('utf-8-sig').splitlines()
    except UnicodeError:return 'unknown'
    if not lines or all(not line for line in lines):return 'missing'
    if len(lines)!=1:return 'conflicting'
    match=re.fullmatch('OWNED_CAPTURE_FIXTURE_FAILED case=([a-z-]+)',lines[0])
    if not match:return 'unknown'
    candidate=match[1]
    value=empty_preflight();value['failureCase']=candidate
    try:schema.preflight_safe(value)
    except ValueError:return 'unknown'
    return candidate


def empty_preflight():
    return {'schema':1,'kind':'owned-hook-preflight','stages':{key:{'runtimeExecuted':False,'exitCode':None,'passed':False,'code':'not-run'} for key in ('parse','fixture')},'passed':False,'casesExpected':57,'casesCompleted':0,'failureCase':'not-observed','runtimeVersions':{'python':None,'powershell':None}}


def preflight(source,control,root):
    verify_source_control(source,control)
    manifest=overlay.manifest_safe(report.read(root/'safe/owned-overlay-identity.json'));overlay.verify(source,control,root,manifest)
    value=empty_preflight();target=root/'safe/owned-hook-preflight.json'
    private=root/'owned-preflight';private.mkdir(exist_ok=False)
    previous=os.environ.get('OWNED_OVERLAY_DIRECTORY');os.environ['OWNED_OVERLAY_DIRECTORY']=str(root/'owned-harness')
    checkpoint(target,schema.preflight_safe(value))
    try:
        value['runtimeVersions']={'python':'.'.join(map(str,sys.version_info[:3])),'powershell':os.environ.get('OWNED_PS_VERSION')}
        checkpoint(target,schema.preflight_safe(value))
        schema.need(value['runtimeVersions']['python']=='3.13.17' and value['runtimeVersions']['powershell'] is not None and os.environ.get('OWNED_PS_IDENTITY_MATCH')=='true')
        for key,name,marker in (('parse','parse-owned-sources.ps1','OWNED_CAPTURE_PARSE_PASS files=3'),('fixture','owned-capture.test.ps1','OWNED_CAPTURE_FIXTURE_PASS cases=57')):
            folder=private/key;folder.mkdir(exist_ok=False)
            value['stages'][key]={'runtimeExecuted':None,'exitCode':None,'passed':False,'code':'wrapper-uncertain'};checkpoint(target,schema.preflight_safe(value))
            value['stages'][key]=powershell_preflight.run_stage(control/'diagnostics/pr200-fresh'/name,marker,folder)
            if key=='fixture':
                stage=value['stages'][key]
                if stage['passed']:value.update(casesCompleted=57,failureCase='not-applicable')
                elif stage['code']=='fixture-rejected':
                    try:
                        with (folder/'stdout.log').open('rb') as stream:value['failureCase']=fixture_failure(stream.read(4097))
                    except Exception:value['failureCase']='missing'
            value['passed']=all(s['passed'] for s in value['stages'].values());checkpoint(target,schema.preflight_safe(value))
            if not value['stages'][key]['passed']:break
    finally:
        if previous is None:os.environ.pop('OWNED_OVERLAY_DIRECTORY',None)
        else:os.environ['OWNED_OVERLAY_DIRECTORY']=previous
    print('OWNED_HOOK_PREFLIGHT_PASS' if value['passed'] else 'OWNED_HOOK_PREFLIGHT_STOP')
    return 0 if value['passed'] else 1


def prepare(source,control,root):
    verify_source_control(source,control)
    manifest=overlay.prepare(source,control,root)
    report.write(root/'safe/owned-overlay-identity.json',manifest)
    report.write(root/'safe/owned-collection.json',schema.placeholder())
    report.write(root/'safe/owned-capture-verdict.json',{'schema':1,'instrumented':True,'captureComplete':False,'oldContinue':None,'scopePassed':False,'originalFailurePreserved':True,'pr200Blocked':True,'status':'not-reached'})
    print('OWNED_INSTRUMENTED_OVERLAY_PREPARED')
    return 0


def trial(source,control,root):
    verify_source_control(source,control)
    schema.need(schema.preflight_safe(report.read(root/'safe/owned-hook-preflight.json'))['passed'])
    manifest=overlay.manifest_safe(report.read(root/'safe/owned-overlay-identity.json'));overlay.verify(source,control,root,manifest)
    previous=os.environ.get('RESCUE_OWNED_CAPTURE');os.environ['RESCUE_OWNED_CAPTURE']='1'
    try:
        code=phase2.trial(source,control,root,1,supervisor_path=root/'owned-harness/supervise.ps1',harness_path=root/'owned-harness')
    finally:
        if previous is None:os.environ.pop('RESCUE_OWNED_CAPTURE',None)
        else:os.environ['RESCUE_OWNED_CAPTURE']=previous
    old=phase2.trial_safe(report.read(root/'safe/trial.json'));capture=schema.placeholder();status='not-reached'
    file=root/'single-trial/output/owned-collection.json'
    if file.exists():
        try:capture=schema.capture_safe(report.read(file));status='observed'
        except Exception:capture['code']='capture-unavailable';status='capture-unavailable'
    else:
        native=report.read(root/'safe/observation/native-result.json') if (root/'safe/observation/native-result.json').exists() else {}
        if native.get('failurePhase') not in {'environment','webview-and-ipc','ownership-confirmation'}:
            capture['code']='capture-unavailable';status='capture-unavailable'
    try:overlay.verify(source,control,root,manifest)
    except Exception:capture['complete']=False;status='overlay-drift'
    report.write(root/'safe/owned-collection.json',schema.capture_safe(capture))
    passed=capture['complete'] and old['continue']
    report.write(root/'safe/owned-capture-verdict.json',schema.verdict_safe({'schema':1,'instrumented':True,'captureComplete':capture['complete'],'oldContinue':old['continue'],'scopePassed':passed,'originalFailurePreserved':True,'pr200Blocked':True,'status':status}))
    print('OWNED_INSTRUMENTED_TRIAL_COMPLETE' if passed else 'OWNED_INSTRUMENTED_TRIAL_STOPPED')
    return 0 if code==0 and passed else 1


def main():
    parser=argparse.ArgumentParser();parser.add_argument('stage',choices=['verify','prepare','preflight','trial'])
    for name in ('source','control','root'):parser.add_argument('--'+name,type=Path,required=True)
    args=parser.parse_args();source,control,root=(getattr(args,k).resolve() for k in ('source','control','root'))
    try:
        if args.stage=='verify':verify_source_control(source,control);print('OWNED_SOURCE_CONTROL_VERIFIED');return 0
        return globals()[args.stage](source,control,root)
    except Exception:
        print('OWNED_CONTROL_STOPPED')
        return 1


if __name__=='__main__':sys.exit(main())

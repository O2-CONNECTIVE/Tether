"""Exact opt-in harness overlay; ordinary product checkout/build remain fixed."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import owned_schema as schema

PRODUCT='c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da'
TREE='e278496a55b4accc6caafa7a7a99538e5c8ceeca'
NATIVE_FILES=tuple('app-startup-contract.test.ps1 app-startup-files.ts app-startup-observation.ps1 app-startup-stages.ts assert-unwired.ts close-observation.ps1 inspect-app-startup.ts inspect-driver-trace.ps1 inventory.ps1 keyboard.ts lineage-baseline.test.ps1 observation.ts process-contract.test.ps1 processes.ps1 refresh-observer.ts run.ts session-observation.ts setup.ps1 startup-logs.ts startup-observation.ps1 supervise.ps1 tools.json webdriver.ts'.split())
HOOK_FILES=('owned-capture.ps1','owned-native.cs')
PRELUDE='''# BEGIN_OWNED_CAPTURE_INIT
$ownedCaptureState = $null
if ($env:RESCUE_OWNED_CAPTURE -ceq '1') {
    . "$PSScriptRoot/owned-capture.ps1"
    Initialize-OwnedNative
}
# END_OWNED_CAPTURE_INIT
'''
WRAP_START='''# BEGIN_OWNED_CAPTURE_OUTER
try {
# END_OWNED_CAPTURE_OUTER
'''
HOOK='''# BEGIN_OWNED_CAPTURE_HOOK
    if ($env:RESCUE_OWNED_CAPTURE -ceq '1') {
        try {
            $ownedCaptureState = & {
                param($OwnedChildren, $OwnedApp, $OwnedHandles, $OwnedRoot, $OwnedOutput)
                $ownedTimer = [Diagnostics.Stopwatch]::StartNew()
                try { $ownedRecord = Invoke-OwnedCollectionCapture $OwnedChildren $OwnedApp $OwnedHandles $OwnedRoot }
                catch { $ownedRecord = New-OwnedCaptureRecord; $ownedRecord.code = 'hook-failed' }
                $ownedTimer.Stop()
                $ownedRecord.elapsedMs = [int][Math]::Min(60000, $ownedTimer.ElapsedMilliseconds)
                $ownedRecord.elapsedClamped = $ownedTimer.ElapsedMilliseconds -gt 60000
                if ($ownedRecord.overrun) { $ownedRecord.complete = $false }
                if ($ownedTimer.ElapsedMilliseconds -gt 2000) { $ownedRecord.overrun = $true; $ownedRecord.complete = $false; if ($ownedRecord.code -ceq 'ok') { $ownedRecord.code = 'overrun' } }
                return $ownedRecord
            } $children $app $childHandles $env:NATIVE_PROFILE $OutputDirectory
        } catch { $ownedCaptureState = $null }
    }
# END_OWNED_CAPTURE_HOOK
'''
PERSIST='''# BEGIN_OWNED_CAPTURE_PERSIST
} finally {
    if ($env:RESCUE_OWNED_CAPTURE -ceq '1' -and $null -ne $ownedCaptureState) {
        try { Save-OwnedCaptureRecord $ownedCaptureState $OutputDirectory; Write-OwnedPersistStatus 'ok' }
        catch { try { Write-OwnedPersistStatus 'uncertain' } catch { } }
    }
}
# END_OWNED_CAPTURE_PERSIST
'''
EXIT_SEAM="if ($report.cleanupFailures -gt 0 -or $report.nativeTestsRun -ne 1 -or $report.nativeTestsPassed -ne 1 -or -not $report.normalCloseVerified) { exit 1 }\n"
INIT_SEAM='$diagnosticBudget = [Diagnostics.Stopwatch]::new()\n'
HOOK_SEAM='    $childHandles = @(Open-ChildObservationHandles $children)\n'


def digest(b):return hashlib.sha256(b).hexdigest()


def git(root,*args):return subprocess.run(['git','-C',str(root),*args],check=True,capture_output=True,timeout=10).stdout


def overlay_bytes(original):
    text=original.decode('utf-8');crlf='\r\n' in text;text=text.replace('\r\n','\n')
    schema.need(text.count(INIT_SEAM)==text.count(HOOK_SEAM)==text.count(EXIT_SEAM)==1 and 'BEGIN_OWNED_CAPTURE' not in text)
    changed=text.replace(INIT_SEAM,INIT_SEAM+PRELUDE+WRAP_START).replace(HOOK_SEAM,HOOK_SEAM+HOOK).replace(EXIT_SEAM,PERSIST+EXIT_SEAM)
    schema.need(changed.replace(PRELUDE,'').replace(WRAP_START,'').replace(HOOK,'').replace(PERSIST,'')==text)
    return changed.replace('\n','\r\n').encode() if crlf else changed.encode()


def manifest_safe(v):
    fields={'schema','instrumented','ordinaryProductUnchanged','productCommit','productTree','controlCommit','controlTree','contractCommit','contractTree','nativeTree','budgetMs','originalSupervisorSha256','overlaySupervisorSha256','hookSha256','typeSha256','files'}
    schema.need(isinstance(v,dict) and set(v)==fields and type(v['schema']) is int and v['schema']==1 and v['instrumented'] is True and v['ordinaryProductUnchanged'] is True)
    schema.need(v['productCommit']==PRODUCT and v['productTree']==TREE and v['contractCommit']=='613f4ff550cd65334b00b262a0beebbc3e069fad' and v['contractTree']=='627bdb664be8f3a9b807077466c7473e915387b2')
    for key in ('controlCommit','controlTree','nativeTree'):schema.need(isinstance(v[key],str) and re.fullmatch('[0-9a-f]{40}',v[key]))
    for key in ('originalSupervisorSha256','overlaySupervisorSha256','hookSha256','typeSha256'):schema.need(isinstance(v[key],str) and re.fullmatch('[0-9a-f]{64}',v[key]))
    schema.need(type(v['budgetMs']) is int and v['budgetMs']==2000 and isinstance(v['files'],dict) and set(v['files'])==set(NATIVE_FILES+HOOK_FILES))
    schema.need(all(isinstance(x,str) and re.fullmatch('[0-9a-f]{64}',x) for x in v['files'].values()))
    schema.need(v['files']['supervise.ps1']==v['overlaySupervisorSha256'] and v['files']['owned-capture.ps1']==v['hookSha256'] and v['files']['owned-native.cs']==v['typeSha256'])
    return v


def prepare(source,control,root):
    schema.need(git(source,'rev-parse','HEAD').decode().strip()==PRODUCT and git(source,'rev-parse','HEAD^{tree}').decode().strip()==TREE)
    folder=root/'owned-harness';folder.mkdir(exist_ok=False)
    native=source/'ui/tauri/native-smoke'
    for name in NATIVE_FILES:
        original=git(source,'show',PRODUCT+':ui/tauri/native-smoke/'+name);actual=(native/name).read_bytes()
        schema.need(actual in (original,original.replace(b'\n',b'\r\n')))
        (folder/name).write_bytes(actual)
    supervisor=(native/'supervise.ps1').read_bytes();(folder/'supervise.ps1').write_bytes(overlay_bytes(supervisor))
    for name in HOOK_FILES:
        relative='diagnostics/pr200-fresh/'+name;actual=(control/relative).read_bytes();original=git(control,'show','HEAD:'+relative)
        schema.need(actual in (original,original.replace(b'\n',b'\r\n')));(folder/name).write_bytes(actual)
    return manifest_safe({'schema':1,'instrumented':True,'ordinaryProductUnchanged':True,'productCommit':PRODUCT,'productTree':TREE,
        'controlCommit':git(control,'rev-parse','HEAD').decode().strip(),'controlTree':git(control,'rev-parse','HEAD^{tree}').decode().strip(),
        'contractCommit':'613f4ff550cd65334b00b262a0beebbc3e069fad','contractTree':'627bdb664be8f3a9b807077466c7473e915387b2',
        'nativeTree':git(source,'rev-parse',PRODUCT+':ui/tauri/native-smoke').decode().strip(),'budgetMs':2000,
        'originalSupervisorSha256':digest(supervisor),'overlaySupervisorSha256':digest((folder/'supervise.ps1').read_bytes()),
        'hookSha256':digest((folder/'owned-capture.ps1').read_bytes()),'typeSha256':digest((folder/'owned-native.cs').read_bytes()),
        'files':{name:digest((folder/name).read_bytes()) for name in NATIVE_FILES+HOOK_FILES}})


def verify(source,control,root,manifest):
    manifest_safe(manifest)
    schema.need(git(control,'rev-parse','HEAD').decode().strip()==manifest['controlCommit'] and git(control,'rev-parse','HEAD^{tree}').decode().strip()==manifest['controlTree'])
    schema.need(not git(control,'status','--porcelain','--untracked-files=all').strip())
    native=source/'ui/tauri/native-smoke';folder=root/'owned-harness'
    schema.need(set(p.name for p in folder.iterdir())==set(NATIVE_FILES+HOOK_FILES))
    for name in NATIVE_FILES+HOOK_FILES:schema.need(digest((folder/name).read_bytes())==manifest['files'][name])
    original=(native/'supervise.ps1').read_bytes();schema.need(digest(original)==manifest['originalSupervisorSha256'] and overlay_bytes(original)==(folder/'supervise.ps1').read_bytes())
    for name in NATIVE_FILES:
        blob=git(source,'show',PRODUCT+':ui/tauri/native-smoke/'+name);actual=(native/name).read_bytes();schema.need(actual in (blob,blob.replace(b'\n',b'\r\n')))
        if name!='supervise.ps1':schema.need(actual==(folder/name).read_bytes())
    for name in HOOK_FILES:schema.need((folder/name).read_bytes()==(control/'diagnostics/pr200-fresh'/name).read_bytes())
    return True

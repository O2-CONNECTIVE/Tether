"""Reproducible source-manifest-bound cross build only; never executes Windows.
No git commit is fabricated for an isolated source snapshot.
"""
from pathlib import Path
import os,subprocess,json,hashlib,shutil,sys
sys.dont_write_bytecode=True
from bundle_bytes import write_utf8_lf
r=Path(__file__).resolve().parent
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
expected=json.loads((r/'source-manifest.json').read_text())
actual={str(p.relative_to(r)):sha(p) for p in sorted(r.rglob('*')) if p.is_file() and 'artifacts' not in p.relative_to(r).parts and p.name!='source-manifest.json'}
if actual!=expected:raise SystemExit('Source manifest mismatch; refuse artifact production')
go=os.environ.get('GO','go')
env=dict(os.environ,GOTOOLCHAIN='local',GOWORK='off',GOENV='off',GOFLAGS='-mod=readonly',GOEXPERIMENT='',GOOS='windows',GOARCH='amd64',GOAMD64='v1',CGO_ENABLED='0')
version=subprocess.check_output([go,'version'],env=env,text=True)
if not version.startswith('go version go1.27.3 '):raise SystemExit('Exact Go1.27.3 required')
subprocess.run([go,'mod','verify'],cwd=r,env=env,check=True)
out=r/'artifacts'
if out.exists():raise SystemExit('Artifact directory exists; no overwrite/retry')
out.mkdir()
subprocess.run([go,'build','-trimpath','-buildvcs=false','-ldflags=-buildid=','-o',str(out/'scm-token-observer.exe'),'.'],cwd=r,env=env,check=True)
for name in ('Invoke-ProtectionProbe.ps1','README.md','SETUP-CONTRACT.md','LICENSE.RescueHost'):shutil.copyfile(r/name,out/name)
goro=Path(subprocess.check_output([go,'env','GOROOT'],env=env,text=True).strip());mod=Path(subprocess.check_output([go,'env','GOMODCACHE'],env=env,text=True).strip())
shutil.copyfile(goro/'LICENSE',out/'LICENSE.Go');shutil.copyfile(mod/'golang.org/x/sys@v0.49.0/LICENSE',out/'LICENSE.x-sys')
files={p.name:sha(p) for p in sorted(out.iterdir()) if p.is_file()}
m={'schema':1,'sourceTreeSha256':sha(r/'source-manifest.json'),'executableSha256':files['scm-token-observer.exe'],'toolchain':'go1.27.3','goos':'windows','goarch':'amd64','signed':False,'windowsExecuted':False,'files':files}
write_utf8_lf(out/'manifest.json',json.dumps(m,indent=2)+'\n')
files['manifest.json']=sha(out/'manifest.json');write_utf8_lf(out/'SHA256SUMS',''.join(v+'  '+k+'\n' for k,v in sorted(files.items())))
print(json.dumps({'sourceTreeSha256':m['sourceTreeSha256'],'executableSha256':m['executableSha256'],'packetSha256':files['manifest.json'],'windowsExecuted':False}))

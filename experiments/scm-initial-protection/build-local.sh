#!/bin/sh
# Mac等でのcross build専用。生成したWindows artifactは実行しない。
set -eu
cd "$(dirname "$0")"
: "${GO:?既存のGo 1.27.3実行物をGOへ指定してください}"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=0
export GOWORK=off GOENV=off GOFLAGS='' GOEXPERIMENT=''
export GOAMD64=v1 GOARM64=v8.0
case "$("$GO" version)" in 'go version go1.27.3 '*) ;; *) exit 2 ;; esac
if [ -n "$(git status --porcelain -- .)" ]; then
  echo 'sourceを固定してからartifactを作成してください' >&2
  exit 2
fi
source_commit=$(git rev-parse HEAD)
"$GO" mod verify
mkdir -p artifacts
GOOS=windows GOARCH=amd64 "$GO" build -mod=readonly -trimpath -buildvcs=false -ldflags=-buildid= -o artifacts/scm-protection-probe.exe .
GOOS=windows GOARCH=arm64 "$GO" build -mod=readonly -trimpath -buildvcs=false -ldflags=-buildid= -o artifacts/scm-protection-probe-arm64-compile-only.exe .
go_root=$("$GO" env GOROOT)
module_cache=$("$GO" env GOMODCACHE)
cp "$go_root/LICENSE" artifacts/LICENSE.Go
cp "$module_cache/golang.org/x/sys@v0.49.0/LICENSE" artifacts/LICENSE.x-sys
cp ../../LICENSE artifacts/LICENSE.RescueHost
cp Invoke-ProtectionProbe.ps1 README.md SETUP-CONTRACT.md artifacts/
python3 - "$source_commit" <<'PY'
import hashlib, json, pathlib, sys
p = pathlib.Path('artifacts')
hashes = {f.name: hashlib.sha256(f.read_bytes()).hexdigest() for f in sorted(p.iterdir()) if f.is_file() and f.name not in {'manifest.json', 'SHA256SUMS'}}
m = {'schema': 1, 'sourceCommit': sys.argv[1], 'executableSha256': hashes['scm-protection-probe.exe'], 'toolchain': 'go1.27.3', 'goos': 'windows', 'goarch': 'amd64', 'signed': False, 'windowsExecuted': False, 'files': hashes}
(p/'manifest.json').write_text(json.dumps(m, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')
hashes['manifest.json'] = hashlib.sha256((p/'manifest.json').read_bytes()).hexdigest()
(p/'SHA256SUMS').write_text(''.join(f'{digest}  {name}\n' for name, digest in sorted(hashes.items())), encoding='utf-8')
print(json.dumps({'sourceCommit':m['sourceCommit'],'executableSha256':m['executableSha256'],'windowsExecuted':False}))
PY

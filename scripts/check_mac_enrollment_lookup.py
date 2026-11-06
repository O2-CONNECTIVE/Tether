"""Compile/replay the private Mac selector against synthetic OS lookup providers.

Explicit fixture check, not a producer launcher or a real enrollment probe.
No OS account database, policy, key, GUI or network is consulted by the probe.
"""
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
ROLE = './internal/residentmac'
USER_FIXTURE = '''package user
import ("os"; "strconv"; "errors")
func current() (*User,error) { return &User{Uid:strconv.Itoa(os.Getuid()),HomeDir:"/fixture/cached"},nil }
func lookupUserId(uid string) (*User,error) { return &User{Uid:uid,HomeDir:"/fixture/fresh"},nil }
func lookupUser(string) (*User,error) { return nil,errors.New("fixture lookup forbidden") }
func lookupGroup(string) (*Group,error) { return nil,errors.New("fixture group forbidden") }
func lookupGroupId(string) (*Group,error) { return nil,errors.New("fixture group forbidden") }
'''
NATIVE_FIXTURE = '''
#include <stdlib.h>
static unsigned fixture_calls = 0;
static int fixture_getpwuid_r(uid_t uid, struct passwd *p, char *buf, size_t size, struct passwd **found) {
 const char *mode=getenv("ENROLLMENT_LOOKUP_FIXTURE");
 const char *home=(++fixture_calls==1) ? "/fixture/first" : "/fixture/second";
 if(mode==NULL)return EINVAL;
 if(strcmp(mode,"error")==0)return EIO;
 if(strcmp(mode,"missing")==0){*found=NULL;return 0;}
 if(strcmp(mode,"outside")==0){p->pw_uid=uid;p->pw_dir=(char*)home;*found=p;return 0;}
 if(strcmp(mode,"unterminated")==0){memset(buf,'x',size);p->pw_uid=uid;p->pw_dir=buf;*found=p;return 0;}
 if(strcmp(mode,"oversized")==0){memset(buf,'x',2048);buf[2048]=0;p->pw_uid=uid;p->pw_dir=buf;*found=p;return 0;}
 if(strcmp(mode,"empty")==0)home="";
 if(strcmp(mode,"relative")==0)home="relative";
 if(strcmp(mode,"root")==0)home="/";
 size_t n=strlen(home)+1;if(n>size)return ERANGE;
 memcpy(buf,home,n);p->pw_uid=uid+(strcmp(mode,"uid")==0);p->pw_dir=buf;*found=p;return 0;
}
#define getpwuid_r fixture_getpwuid_r
'''
PROBE = '''package residentmac
import ("context";"errors";"testing";"time")
func TestNativeEnrollmentLookupReplay(t *testing.T) {
 ctx,cancel:=context.WithTimeout(context.Background(),time.Minute);defer cancel()
 t.Setenv("ENROLLMENT_LOOKUP_FIXTURE","success")
 t.Setenv("HOME","/fixture/environment")
 for _,home:=range []string{"/fixture/first","/fixture/second"} {
  got,err:=currentEnrolledPolicy(ctx)
  want:=home+"/Library/Application Support/com.o2connective.rescue-host.shell/resident-policy.json"
  if err!=nil || got!=want { t.Fatalf("fresh lookup: got=%q err=%v want=%q",got,err,want) }
 }
 for _,mode:=range []string{"error","missing","uid","outside","unterminated","oversized","empty","relative","root"} {
  t.Run(mode,func(t *testing.T){
   t.Setenv("ENROLLMENT_LOOKUP_FIXTURE",mode)
   got,err:=currentEnrolledPolicy(ctx)
   if got!="" || !errors.Is(err,ErrStandbyUnavailable) {t.Fatalf("unproven OS record accepted: %q %v",got,err)}
  })
 }
}
'''


def command(arguments, env, cwd=None):
    process = subprocess.Popen(arguments, cwd=cwd, env=env,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    interrupted = False
    while True:
        try:
            stdout, _ = process.communicate()
            break
        except KeyboardInterrupt:
            # Keep the same compiler/test driver until it exits; do not kill
            # only its parent, replace it, or clean its live source/cache.
            interrupted = True
    if interrupted or process.returncode:
        raise ValueError('fixture driver failed or interrupted')
    return stdout


def run():
    if len(sys.argv) != 1 or sys.platform != 'darwin':
        raise ValueError('unsupported fixture invocation')
    env = {key: value for key, value in os.environ.items()
           if key in ('PATH', 'HOME', 'TMPDIR', 'GOMODCACHE')}
    env.update(GOTOOLCHAIN='local', GOENV='off', GOWORK='off', GOPROXY='off',
               GOSUMDB='off', GOFLAGS='', CGO_ENABLED='1')
    arch = {'arm64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine())
    if arch is None:
        raise ValueError('unsupported fixture architecture')
    env.update(GOOS='darwin', GOARCH=arch)
    source = ROOT / 'backend/internal/residentmac/enrollment_darwin.go'
    with tempfile.TemporaryDirectory(prefix='enrollment-lookup-') as temporary:
        directory = Path(temporary)
        env['GOCACHE'] = str(directory / 'cache')
        # Use the caller's existing cache read-only/offline; no install/download.
        metadata = json.loads(command(['go', 'env', '-json', 'GOROOT', 'GOVERSION'], env))
        if metadata['GOVERSION'] != 'go1.27.3':
            raise ValueError('unreviewed fixture compiler')
        native = source.read_text()
        marker = '// ACCOUNT_LOOKUP_CALLSITE'
        if native.count(marker) != 1:
            raise ValueError('native replay seam changed')
        files = {'user.go': USER_FIXTURE, 'probe_test.go': PROBE,
                 'native.go': native.replace(marker, NATIVE_FIXTURE + '\n' + marker)}
        for name, content in files.items():
            (directory / name).write_text(content)
        replacements = {
            str(Path(metadata['GOROOT']) / 'src/os/user/cgo_lookup_unix.go'): str(directory / 'user.go'),
            str(source.with_name('enrollment_lookup_probe_test.go')): str(directory / 'probe_test.go'),
            str(source): str(directory / 'native.go'),
        }
        overlay = directory / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': replacements}))
        arguments = ['go', 'test', '-mod=readonly', '-p=1', '-count=1', '-timeout=1m', '-json',
                   '-overlay', str(overlay), '-run', '^TestNativeEnrollmentLookupReplay$', ROLE]
        output = command(arguments, env, ROOT / 'backend')
        events = [json.loads(line) for line in output.splitlines() if line.startswith('{')]
        passed = sum(e.get('Action') == 'pass' and 'Test' in e for e in events)
        bad = any(e.get('Action') in ('fail', 'skip') for e in events)
        if bad or passed != 10:
            raise ValueError('native lookup replay failed')
    print(json.dumps({'fixture': 'native-enrollment-lookup', 'passed': passed,
                      'realAccountDatabaseRead': False, 'policyOrKeyRead': False}))


if __name__ == '__main__':
    try:
        run()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        print('NATIVE_ENROLLMENT_LOOKUP_FIXTURE_FAILED', file=sys.stderr)
        raise SystemExit(1)

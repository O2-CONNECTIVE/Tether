#!/usr/bin/env python3
"""Local-only bundle; no build, signing, install, launch, or network."""
import hashlib
import json
import os
from pathlib import Path
import plistlib
import stat
import struct
import sys

NOTICE = """Tether Status - LOCAL EVALUATION ONLY
Read-only viewer; no agent, consent, authentication, or command authority.
No distribution identity signing or notarization is performed by this script.
The compiler/linker may have attached its default local ad-hoc signature.
Do not distribute this debug artifact: dependency/license inventory and release
notices remain a release gate. No runtime keys, configuration or agent are included.
macOS 13 is declared; validation is on the current development Mac only.
Closing this viewer does not stop an agent. Missing stdin is unavailable, not safe.
"""
EXPECTED = {
    "CFBundleDevelopmentRegion":"ja", "CFBundleExecutable":"TetherStatus",
    "CFBundleIdentifier":"local.tether.status.evaluation", "CFBundleName":"Tether Status",
    "CFBundleDisplayName":"Tether Status — Local Evaluation", "CFBundlePackageType":"APPL",
    "CFBundleShortVersionString":"0.0.0", "CFBundleVersion":"1",
    "LSMinimumSystemVersion":"13.0", "NSHighResolutionCapable":True
}
def regular_bytes(path, limit):
    fd=os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise ValueError("nonregular input")
        with os.fdopen(fd, "rb", closefd=False) as stream:
            value=stream.read(limit+1)
        if len(value)>limit:raise ValueError("input bound")
        return value
    finally:os.close(fd)

def inspect_binary(data):
    if len(data)<32:raise ValueError("Mach-O header")
    magic,cpu,subcpu,kind,ncmd,size,flags,reserved=struct.unpack_from("<8I",data)
    if magic!=0xfeedfacf or cpu!=0x0100000c or kind!=2 or size>len(data)-32:
        raise ValueError("expected arm64 executable")
    offset=32;minimum=None
    if ncmd>4096:raise ValueError("command bound")
    for _ in range(ncmd):
        if offset+8>32+size:raise ValueError("load command")
        cmd,length=struct.unpack_from("<II",data,offset)
        if length<8 or length%8!=0 or offset+length>32+size:raise ValueError("load command size")
        if cmd==0x32:
            if length<24 or minimum is not None:raise ValueError("build version")
            platform,minimum,sdk,ntools=struct.unpack_from("<4I",data,offset+8)
            if ntools>4096 or length!=24+8*ntools:raise ValueError("build tool table")
            if platform!=1:raise ValueError("not macOS")
        offset+=length
    if offset!=32+size or minimum!=0x000d0000:
        raise ValueError("minimum OS must match 13.0")

def package(root):
    root=Path(root).absolute()
    # Trusted local workspace only; refuse symlink components below its root.
    for relative in [".build", ".build/arm64-apple-macosx", ".build/arm64-apple-macosx/debug", "Packaging"]:
        p=root/relative
        if p.is_symlink() or not p.is_dir():raise ValueError("input boundary")
    source=root/".build/arm64-apple-macosx/debug/TetherStatus"
    binary=regular_bytes(source,64*1024*1024)
    inspect_binary(binary)
    plist=plistlib.loads(regular_bytes(root/"Packaging/Info.plist",16384))
    if plist!=EXPECTED:raise ValueError("plist contract")
    parent=root/".build/local-evaluation"
    if parent.is_symlink():raise ValueError("output boundary")
    parent.mkdir(exist_ok=True)
    manifest=parent/"bundle-manifest.json"
    if os.path.lexists(manifest):raise FileExistsError("manifest exists")
    dest=parent/"Tether Status.app"
    dest.mkdir()  # Existing object of any type stops; never cleanup/overwrite it.
    contents=dest/"Contents"
    (contents/"MacOS").mkdir(parents=True);(contents/"Resources").mkdir()
    payloads={"Contents/MacOS/TetherStatus":binary,
              "Contents/Info.plist":plistlib.dumps(plist),
              "Contents/Resources/LOCAL-EVALUATION.txt":NOTICE.encode()}
    for name,value in payloads.items():
        with (dest/name).open("xb") as stream:stream.write(value)
    os.chmod(contents/"MacOS/TetherStatus",0o755)
    entries={name:hashlib.sha256(value).hexdigest() for name,value in sorted(payloads.items())}
    with manifest.open("x") as stream:stream.write(json.dumps(entries,indent=2)+"\n")
    return dest

if __name__=="__main__":
    if len(sys.argv)!=1:raise SystemExit(64)
    try:package(Path(__file__).resolve().parents[1])
    except (OSError,ValueError,plistlib.InvalidFileException):
        print("PACKAGE_FAILED");raise SystemExit(1)
    print("PACKAGE_LOCAL_EVALUATION_READY")

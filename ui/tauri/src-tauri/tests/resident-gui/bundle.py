"""Disposable CI wrapper comparison only. No launch, signing, install or upload."""
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import stat
import sys

LIMIT = 512 * 1024 * 1024
NAME = 'TetherCurrentGUIFixture.app'
EXECUTABLE = Path(NAME) / 'Contents/MacOS/resident_gui_ci'
PLIST = {
    'CFBundleExecutable': 'resident_gui_ci',
    'CFBundleIdentifier': 'com.o2connective.rescue-host.shell',
    'CFBundlePackageType': 'APPL', 'CFBundleInfoDictionaryVersion': '6.0',
    'CFBundleName': 'Tether Current GUI CI',
    'CFBundleDisplayName': 'カレントの検証版（合成CI）',
    'CFBundleVersion': '0.1.0', 'CFBundleShortVersionString': '0.1.0',
}


def boundary(path):
    if not path.is_absolute():
        raise ValueError('absolute input required')
    if any(p.is_symlink() for p in [path, *path.parents]):
        raise ValueError('symlink boundary')


def copy_or_hash(path, output=None):
    boundary(path)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= LIMIT:
            raise ValueError('regular bounded binary required')
        digest = hashlib.sha256()
        total = 0
        with os.fdopen(descriptor, 'rb', closefd=False) as source:
            while block := source.read(1024 * 1024):
                total += len(block)
                if total > LIMIT:
                    raise ValueError('binary bound')
                digest.update(block)
                if output is not None:
                    output.write(block)
        if total != info.st_size:
            raise ValueError('input changed')
        return digest.hexdigest()
    finally:
        os.close(descriptor)


def verify(source, root, expected):
    boundary(root)
    target = root / EXECUTABLE
    plist_path = root / NAME / 'Contents/Info.plist'
    boundary(plist_path)
    descriptor = os.open(plist_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            raise ValueError('regular plist required')
        with os.fdopen(descriptor, 'rb', closefd=False) as stream:
            value = stream.read(16385)
        if len(value) > 16384 or value != plistlib.dumps(PLIST):
            raise ValueError('fixed plist required')
    finally:
        os.close(descriptor)
    if copy_or_hash(source) != expected or copy_or_hash(target) != expected:
        raise ValueError('binary identity changed')


def create(source, root, expected):
    boundary(root)
    if not root.is_dir() or not re.fullmatch('[a-f0-9]{64}', expected):
        raise ValueError('fresh root and digest required')
    config = json.loads(Path(__file__).with_name('tauri.conf.json').read_text())
    if (config['identifier'], config['productName'], config['version']) != (
        PLIST['CFBundleIdentifier'], PLIST['CFBundleName'], PLIST['CFBundleVersion']
    ):
        raise ValueError('context metadata changed')
    if copy_or_hash(source) != expected:
        raise ValueError('source digest')
    bundle = root / NAME
    bundle.mkdir()  # Existing output is never removed, replaced or reused.
    contents = bundle / 'Contents'
    (contents / 'MacOS').mkdir(parents=True)
    target = root / EXECUTABLE
    with target.open('xb') as output:
        if copy_or_hash(source, output) != expected:
            raise ValueError('source changed while copying')
        output.flush()
        os.fsync(output.fileno())
    target.chmod(0o755)
    with (contents / 'Info.plist').open('xb') as output:
        output.write(plistlib.dumps(PLIST))
        output.flush()
        os.fsync(output.fileno())
    verify(source, root, expected)


if __name__ == '__main__':
    try:
        if tuple(os.environ.get(k) for k in ('CI_ACTUAL_PRODUCER', 'RUNNER_ENVIRONMENT', 'RUNNER_OS', 'RUNNER_ARCH')) != ('1', 'github-hosted', 'macOS', 'ARM64'):
            raise ValueError('disposable CI only')
        if len(sys.argv) != 4 or sys.argv[1] not in ('create', 'verify'):
            raise ValueError('invocation')
        action = create if sys.argv[1] == 'create' else verify
        action(Path(sys.argv[2]), Path(sys.argv[3]), os.environ['CI_GUI_BINARY_SHA256'])
    except (OSError, ValueError, KeyError, plistlib.InvalidFileException):
        print('GUI_FIXTURE_EVENT=wrapper-invalid')
        raise SystemExit(1)
    print('GUI_FIXTURE_EVENT=wrapper-verified')

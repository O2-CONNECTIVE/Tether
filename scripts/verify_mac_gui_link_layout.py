"""Read two fixed Mac GUI images; never load, launch, sign, assemble or distribute."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import struct

# -I execution deliberately excludes the caller's import path.
import sys
sys.path.insert(0, str(Path(__file__).resolve().parent))
import package_mac_agent as files

IMAGES = {'gui': 'Contents/MacOS/rescue-host-shell',
          'native': 'Contents/Frameworks/libResidentPreparation.dylib'}
NATIVE = '@rpath/libResidentPreparation.dylib'
FRAMEWORKS = '@executable_path/../Frameworks'
SWIFT_ROOT = '/usr/lib/swift'
LIMIT = 256 * 1024 * 1024
# This finite set is a layout contract, not a general Mach-O loader validator.
OPAQUE = {0x2, 0xb, 0x19, 0x1a, 0x1b, 0x1d, 0x22, 0x80000022, 0x26,
          0x29, 0x2a, 0x80000028, 0x80000033, 0x80000034}
GAPS = ['binary-source-correspondence', 'fixture-free-build', 'dynamic-loader-validation',
        'system-library-availability', 'toolchain-sdk-integrity', 'notice-closure',
        'binary-secret-path-audit', 'publisher-signature', 'notarization',
        'verify-to-exec', 'runtime-compatibility', 'native-cleanup', 'enrollment']


class LayoutError(Exception):
    pass


def require(condition, code):
    if not condition:
        raise LayoutError(code)


def command_text(command, fixed):
    require(len(command) >= fixed, 'LINK_COMMAND_INVALID')
    start = struct.unpack_from('<I', command, 8)[0]
    require(start == fixed and start < len(command), 'LINK_STRING_INVALID')
    stop = command.find(b'\0', start)
    require(stop > start and not any(command[stop:]), 'LINK_STRING_INVALID')
    try:
        value = command[start:stop].decode('ascii')
    except UnicodeError:
        raise LayoutError('LINK_STRING_INVALID') from None
    require(len(value) <= 1024 and re.fullmatch(r'[A-Za-z0-9_@./+-]+', value),
            'LINK_STRING_INVALID')
    return value


def system_library(value):
    parts = value.split('/')
    if any(part in ('', '.', '..') for part in parts[1:]):
        return False
    return value.startswith('/usr/lib/') or value.startswith('/System/Library/Frameworks/')


def swift_library(value):
    return re.fullmatch(r'@rpath/libswift[A-Za-z0-9_]+\.dylib', value) is not None


def inspect(data, target, role):
    require(len(data) >= 32, 'LINK_MACHO_INVALID')
    magic, cpu, subtype, kind, count, size, flags, reserved = struct.unpack_from('<8I', data)
    require(magic == 0xfeedfacf and cpu == files.TARGETS[target] and reserved == 0 and
            kind == (2 if role == 'gui' else 6) and 0 < count <= 4096 and
            8 * count <= size <= min(len(data) - 32, 1024 * 1024), 'LINK_MACHO_INVALID')
    offset, end = 32, 32 + size
    minimum, identity, dependencies, rpaths, linker = None, [], [], [], []
    for _ in range(count):
        require(offset + 8 <= end, 'LINK_COMMAND_INVALID')
        code, length = struct.unpack_from('<2I', data, offset)
        require(length >= 8 and length % 8 == 0 and length <= end - offset,
                'LINK_COMMAND_INVALID')
        command = data[offset:offset + length]
        if code == 0x32:
            require(minimum is None and length >= 24, 'LINK_PLATFORM_INVALID')
            platform, minimum, sdk, tools = struct.unpack_from('<4I', command, 8)
            require(platform == 1 and 0 < minimum <= sdk and length == 24 + tools * 8,
                    'LINK_PLATFORM_INVALID')
            seen = set()
            for index in range(tools):
                tool, version = struct.unpack_from('<2I', command, 24 + index * 8)
                require(tool in (1, 2, 3) and tool not in seen and version > 0,
                        'LINK_PLATFORM_INVALID')
                seen.add(tool)
        elif code in (0xc, 0x80000018):
            dependencies.append((code, command_text(command, 24)))
        elif code == 0xd:
            identity.append(command_text(command, 24))
        elif code == 0x8000001c:
            rpaths.append(command_text(command, 12))
        elif code == 0xe:
            linker.append(command_text(command, 12))
        else:
            require(code in OPAQUE, 'LINK_COMMAND_UNSUPPORTED')
        offset += length
    require(offset == end and minimum is not None, 'LINK_PLATFORM_INVALID')
    require(identity == ([] if role == 'gui' else [NATIVE]), 'LINK_IDENTITY_INVALID')
    require(linker in ([], ['/usr/lib/dyld']) if role == 'gui' else not linker,
            'LINK_DYNAMIC_LINKER_INVALID')
    require(len(rpaths) == len(set(rpaths)) and len(dependencies) == len({v for _, v in dependencies}),
            'LINK_DUPLICATE_INVALID')
    allowed_rpaths = {SWIFT_ROOT} | ({FRAMEWORKS} if role == 'gui' else set())
    require(set(rpaths) <= allowed_rpaths and (role != 'gui' or FRAMEWORKS in rpaths),
            'LINK_RPATH_INVALID')
    native_links = [(code, value) for code, value in dependencies if value == NATIVE]
    require(native_links == ([(0xc, NATIVE)] if role == 'gui' else []), 'LINK_NATIVE_DEPENDENCY_INVALID')
    for _, value in dependencies:
        require(value == NATIVE or system_library(value) or swift_library(value),
                'LINK_DEPENDENCY_INVALID')
        if swift_library(value):
            require(SWIFT_ROOT in rpaths, 'LINK_SWIFT_RPATH_MISSING')
    return minimum


def framework_directory(app):
    directory = app / 'Contents/Frameworks'
    files.directory_chain(directory)
    with os.scandir(directory) as entries:
        first = next(entries, None)
        require(first is not None and first.name == 'libResidentPreparation.dylib' and
                next(entries, None) is None, 'LINK_FRAMEWORK_SET_INVALID')


def verify(app, target, hashes):
    require(target in files.TARGETS and set(hashes) == set(IMAGES) and
            all(isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value)
                for value in hashes.values()), 'LINK_EXPECTATION_INVALID')
    app = Path(os.path.abspath(app))
    files.directory_chain(app)
    framework_directory(app)
    minima = []
    for role, path in IMAGES.items():
        data = files.read_regular(app / path, LIMIT)
        require(hashlib.sha256(data).hexdigest() == hashes[role], 'LINK_IMAGE_HASH_MISMATCH')
        minima.append(inspect(data, target, role))
    framework_directory(app)
    return {'check': 'mac-gui-link-layout', 'status': 'blocked', 'linkLayoutMatched': True,
            'target': target, 'imageHashes': hashes, 'minimumMacOSDeclared': files.version_string(max(minima)),
            'imageLoaded': False, 'runtimeVerified': False, 'productionGateClosed': True,
            'unverifiedScopes': GAPS.copy()}


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise LayoutError('LINK_ARGUMENT_INVALID')


def main(argv=None):
    try:
        parser = Parser(description=__doc__, allow_abbrev=False)
        for name in ('app', 'target', 'gui-sha256', 'native-sha256'):
            parser.add_argument('--' + name, required=True)
        args = parser.parse_args(argv)
        result = verify(args.app, args.target, {'gui': args.gui_sha256, 'native': args.native_sha256})
        code = 2
    except (LayoutError, files.PackageError) as error:
        result, code = {'check': 'mac-gui-link-layout', 'status': 'refused', 'code': str(error)}, 1
    except (OSError, ValueError, TypeError, KeyError, OverflowError, struct.error):
        result, code = {'check': 'mac-gui-link-layout', 'status': 'refused', 'code': 'LINK_INPUT_INVALID'}, 1
    print(json.dumps(result, sort_keys=True))
    return code


if __name__ == '__main__':
    raise SystemExit(main())

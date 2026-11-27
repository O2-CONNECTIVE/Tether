"""Read two fixed Mac GUI images; never load, launch, sign, assemble or distribute."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import struct
import plistlib
from xml.etree import ElementTree
from xml.parsers.expat import ExpatError

# -I execution deliberately excludes the caller's import path.
import sys
sys.path.insert(0, str(Path(__file__).resolve().parent))
import package_mac_agent as files

IMAGES = {'gui': 'Contents/MacOS/rescue-host-shell',
          'native': 'Contents/Frameworks/libResidentPreparation.dylib'}
ASSEMBLY_IMAGES = {**IMAGES, 'producer': 'Contents/Helpers/resident-pipe',
                   'hostPair': 'Contents/Helpers/tether-host-pair'}
STANDALONE_HELPERS = {'producer', 'hostPair'}
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


class UniquePlistKeys(dict):
    def __setitem__(self, key, value):
        if key in self:
            raise plistlib.InvalidFileException()
        super().__setitem__(key, value)


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


def macho_commands(data, target, role):
    require(role in ASSEMBLY_IMAGES and len(data) >= 32, 'LINK_MACHO_INVALID')
    magic, cpu, subtype, kind, count, size, flags, reserved = struct.unpack_from('<8I', data)
    require(magic == 0xfeedfacf and cpu == files.TARGETS[target] and reserved == 0 and
            kind == (6 if role == 'native' else 2) and 0 < count <= 4096 and
            8 * count <= size <= min(len(data) - 32, 1024 * 1024), 'LINK_MACHO_INVALID')
    offset, end = 32, 32 + size
    for _ in range(count):
        require(offset + 8 <= end, 'LINK_COMMAND_INVALID')
        code, length = struct.unpack_from('<2I', data, offset)
        require(length >= 8 and length % 8 == 0 and length <= end - offset,
                'LINK_COMMAND_INVALID')
        yield code, data[offset:offset + length]
        offset += length
    require(offset == end, 'LINK_PLATFORM_INVALID')


def inspect(data, target, role):
    minimum, identity, dependencies, rpaths, linker = None, [], [], [], []
    for code, command in macho_commands(data, target, role):
        length = len(command)
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
    require(minimum is not None, 'LINK_PLATFORM_INVALID')
    require(identity == ([NATIVE] if role == 'native' else []), 'LINK_IDENTITY_INVALID')
    require(linker in ([], ['/usr/lib/dyld']) if role != 'native' else not linker,
            'LINK_DYNAMIC_LINKER_INVALID')
    require(len(rpaths) == len(set(rpaths)) and len(dependencies) == len({v for _, v in dependencies}),
            'LINK_DUPLICATE_INVALID')
    allowed_rpaths = set() if role in STANDALONE_HELPERS else {SWIFT_ROOT} | ({FRAMEWORKS} if role == 'gui' else set())
    require(set(rpaths) <= allowed_rpaths and (role != 'gui' or FRAMEWORKS in rpaths),
            'LINK_RPATH_INVALID')
    native_links = [(code, value) for code, value in dependencies if value == NATIVE]
    require(native_links == ([(0xc, NATIVE)] if role == 'gui' else []), 'LINK_NATIVE_DEPENDENCY_INVALID')
    for _, value in dependencies:
        require(system_library(value) or (role not in STANDALONE_HELPERS and
                                          (value == NATIVE or swift_library(value))),
                'LINK_DEPENDENCY_INVALID')
        if swift_library(value):
            require(SWIFT_ROOT in rpaths, 'LINK_SWIFT_RPATH_MISSING')
    return minimum


def embedded_info_plist(data, target):
    payload = None
    error = 'EMBEDDED_INFO_PLIST_INVALID'
    for code, command in macho_commands(data, target, 'gui'):
        if code != 0x19:  # LC_SEGMENT_64
            continue
        require(len(command) >= 72, error)
        if command[8:24].rstrip(b'\0') != b'__TEXT':
            continue
        vm_address, vm_size, file_offset, file_size = struct.unpack_from('<4Q', command, 24)
        count = struct.unpack_from('<I', command, 64)[0]
        require(len(command) == 72 + count * 80, error)
        for index in range(count):
            start = 72 + index * 80
            name, segment, address, size, offset = struct.unpack_from('<16s16sQQI', command, start)
            if name.rstrip(b'\0') != b'__info_plist':
                continue
            flags = struct.unpack_from('<I', command, start + 64)[0]
            load_end = 32 + struct.unpack_from('<I', data, 20)[0]
            relative = offset - file_offset
            require(payload is None and segment.rstrip(b'\0') == b'__TEXT' and
                    flags & 0xff == 0 and 0 < size <= 64 * 1024 and offset >= load_end and
                    file_offset + file_size <= len(data) and 0 <= relative and
                    relative + size <= min(file_size, vm_size) and address == vm_address + relative, error)
            payload = data[offset:offset + size]
    return payload


def xml_plist_dictionary(payload):
    # plistlib permits unknown XML elements and additional top-level values.
    root = ElementTree.fromstring(payload)
    if root.tag != 'plist' or len(root) != 1 or root[0].tag != 'dict':
        return False
    scalar_tags = {'data', 'date', 'false', 'integer', 'real', 'string', 'true'}
    value_tags = scalar_tags | {'array', 'dict'}
    for node in root[0].iter():
        if node.tag == 'dict':
            if len(node) % 2 or any(child.tag != 'key' for child in node[::2]):
                return False
            if any(child.tag not in value_tags for child in node[1::2]):
                return False
        elif node.tag == 'array':
            if any(child.tag not in value_tags for child in node):
                return False
        elif len(node):
            return False
    return True


def embedded_info_plist_identity(data, target, bundle_plist):
    """Report only whether the GUI image declares the same app identity."""
    try:
        payload = embedded_info_plist(data, target)
        if payload is None:
            return 'absent'
        plist = plistlib.loads(payload, dict_type=UniquePlistKeys)
        if not payload.startswith(b'bplist00') and not xml_plist_dictionary(payload):
            return 'invalid'
    except (LayoutError, plistlib.InvalidFileException, ElementTree.ParseError, ExpatError,
            AttributeError, LookupError, ValueError, TypeError, OverflowError, RecursionError):
        return 'invalid'
    identity_keys = ('CFBundleIdentifier', 'CFBundleExecutable', 'CFBundlePackageType')
    if not isinstance(plist, dict) or any(not isinstance(plist.get(key), str) or not plist[key]
                                          for key in identity_keys):
        return 'invalid'
    return 'match' if all(plist[key] == bundle_plist[key] for key in identity_keys) else 'mismatch'


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


def verify_assembly(app, target, hashes):
    """Four fixed product images, not an arbitrary artifact import or launch gate."""
    require(set(hashes) == set(ASSEMBLY_IMAGES), 'ASSEMBLY_EXPECTATION_INVALID')
    app = Path(os.path.abspath(app))
    result = verify(app, target, {role: hashes[role] for role in IMAGES})
    expected = {'Contents': {'MacOS', 'Frameworks', 'Helpers', 'Info.plist'},
                'Contents/MacOS': {'rescue-host-shell'},
                'Contents/Frameworks': {'libResidentPreparation.dylib'},
                'Contents/Helpers': {'resident-pipe', 'tether-host-pair'}}
    files.directory_chain(app)
    require({p.name for p in app.iterdir()} == {'Contents'}, 'ASSEMBLY_INVENTORY_INVALID')
    for relative, names in expected.items():
        directory = app / relative
        files.directory_chain(directory)
        require({p.name for p in directory.iterdir()} == names, 'ASSEMBLY_INVENTORY_INVALID')
    minima = []
    for role, relative in ASSEMBLY_IMAGES.items():
        expected_hash = hashes[role]
        require(isinstance(expected_hash, str) and re.fullmatch('[0-9a-f]{64}', expected_hash),
                'ASSEMBLY_EXPECTATION_INVALID')
        data = files.read_regular(app / relative, LIMIT)
        require(hashlib.sha256(data).hexdigest() == expected_hash, 'LINK_IMAGE_HASH_MISMATCH')
        minima.append(inspect(data, target, role))
    minimum = files.version_string(max(minima))
    plist = plistlib.loads(files.read_regular(app / 'Contents/Info.plist', 64 * 1024))
    require(isinstance(plist, dict) and plist.get('CFBundleExecutable') == 'rescue-host-shell' and
            plist.get('CFBundleIdentifier') == 'com.o2connective.rescue-host.shell' and
            plist.get('CFBundlePackageType') == 'APPL' and
            plist.get('LSMinimumSystemVersion') == minimum, 'ASSEMBLY_PLIST_INVALID')
    return {**result, 'check': 'mac-gui-producer-assembly', 'imageHashes': dict(hashes),
            'minimumMacOSDeclared': minimum, 'producerIncluded': True, 'hostPairIncluded': True}


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

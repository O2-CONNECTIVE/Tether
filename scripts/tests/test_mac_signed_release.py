"""Anonymous compiler/signature observations; never Developer ID or runtime proof."""
import contextlib
import io
import json
import os
from pathlib import Path
import plistlib
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import build_mac_signed_release as tool
from scripts.tests.test_mac_gui_link_layout import image
from scripts.tests.test_mac_sealed_producer_build import executable


class SignedReleaseTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name).resolve()
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        self.output = self.root / 'out'
        self.revision, self.tree = 'a' * 40, 'b' * 40
        self.team, self.cert = 'FIXTURE123', 'C' * 40
        self.events = []
        self.change = lambda stage: None
        self.observe_change = lambda result: None
        self.outer_change = lambda app: None
        self.patch(tool.source, 'ROOT', self.repo)
        self.patch(tool.source, 'head', lambda: (self.revision, self.tree))
        self.patch(tool.platform, 'system', lambda: 'Darwin')
        self.patch(tool.platform, 'machine', lambda: 'arm64')
        self.patch(tool.platform, 'mac_ver', lambda: ('27.1', (), ''))
        self.patch(tool.release, 'snapshot', self.snapshot)
        self.patch(tool.support, 'toolchain', lambda *a: ('/swift', '/sdk', {}))
        self.patch(tool.support, 'build_producer', self.producer)
        self.patch(tool.release, 'compile_observer', lambda *a: Path('/observer'))
        self.patch(tool.release, 'sign', self.sign)
        self.patch(tool.release, 'observe', self.observe)
        self.patch(tool.support, 'compile_images', self.compile)

    def patch(self, owner, name, value):
        item = patch.object(owner, name, value)
        item.start()
        self.addCleanup(item.stop)

    def snapshot(self, revision, stage):
        path = stage / 'ui/tauri/src-tauri/tauri.conf.json'
        path.parent.mkdir(parents=True)
        path.write_bytes(b'{"identifier":"com.o2connective.rescue-host.shell","version":"0.1.0","bundle":{"active":false}}')
        return {'ui/tauri/src-tauri/tauri.conf.json': tool.source.digest(path.read_bytes())}, {}

    def producer(self, stage, revision, tree):
        self.events.append('build-producer')
        path = stage / 'resident-pipe'
        path.write_bytes(executable())
        return path, {'sourceCommit': revision, 'sourceTree': tree, 'image': {'sha256': tool.source.digest(path.read_bytes())}}

    def sign(self, path, role, identity, stage, env):
        self.events.append('sign-' + role)
        if role == 'gui':
            signature = path / 'Contents/_CodeSignature'
            signature.mkdir()
            (signature / 'CodeResources').write_bytes(plistlib.dumps({'files2': {}}))
            executable = path / tool.layout.ASSEMBLY_IMAGES['gui']
            executable.write_bytes(executable.read_bytes() + b'signed-gui')
            self.outer_change(path)
        else:
            path.write_bytes(path.read_bytes() + ('signed-' + role).encode())

    def observe(self, observer, path, role, team, identity, stage, env, cdhash=None):
        self.events.append('observe-' + role)
        result = {'schema': 'tether-mac-release-signature/1', 'teamIdentifier': team,
                  'signingIdentifier': tool.release.IDENTIFIERS[role], 'certificateSha1': identity,
                  'cdhash': ('d' if role == 'producer' else 'e') * 40,
                  'validationCategory': 'developer-id', 'secureTimestamp': True,
                  'hardenedRuntime': True, 'entitlements': {}}
        self.observe_change(result)
        return result

    def compile(self, stage, env, swift, sdk, *, signed_release=False):
        self.events.append('compile-gui-native')
        self.assertTrue(signed_release)
        provider = stage / tool.release.PROVIDER
        self.assertIn(self.team, provider.read_text())
        self.assertIn('0xdd', provider.read_text())
        self.assertEqual(env['MACOSX_DEPLOYMENT_TARGET'], '27.0')
        result = {}
        for role in ('gui', 'native'):
            result[role] = stage / role
            result[role].write_bytes(image(role))
        self.change(stage)
        return result

    def build(self, **kwargs):
        return tool.build(self.output, self.team, self.cert, self.revision, **kwargs)

    def test_producer_is_signed_and_verified_before_compilation(self):
        result = self.build()
        self.assertEqual(self.events[:5], ['build-producer', 'sign-producer', 'observe-producer',
                                         'compile-gui-native', 'sign-native'])
        self.assertEqual(self.events.count('sign-producer'), 1)
        self.assertLess(self.events.index('sign-native'), self.events.index('sign-gui'))
        self.assertEqual(result['activation'], 'unverified')
        self.assertEqual(result['distribution'], 'blocked')
        self.assertFalse(result['notarizationPerformed'])
        self.assertFalse(result['imageExecuted'])
        self.assertEqual(result['approvedProducer']['cdhash'], 'd' * 40)
        self.assertNotEqual(result['preSigningImages']['producer']['sha256'], result['images']['producer']['sha256'])
        self.assertEqual(result['generatedIdentity']['path'], tool.release.PROVIDER)
        self.assertTrue((self.output / tool.release.PROVIDER).is_file())

    def test_wrong_observation_never_reaches_compilation(self):
        cases = [('teamIdentifier', 'OTHER12345'), ('signingIdentifier', tool.release.IDENTIFIERS['gui']),
                 ('certificateSha1', 'A' * 40), ('validationCategory', 'adhoc'),
                 ('secureTimestamp', False), ('hardenedRuntime', False),
                 ('entitlements', {'com.apple.security.cs.disable-library-validation': True}),
                 ('cdhash', '0' * 40), ('cdhash', 'd' * 64), ('cdhash', ['d' * 40]),
                 ('cdhash', 'd' * 39), ('schema', 'untrusted')]
        for key, value in cases:
            with self.subTest(key=key, value=value):
                self.events.clear()
                self.observe_change = lambda result: result.update({key: value})
                with self.assertRaisesRegex(tool.source.BuildError, 'SIGNATURE_OBSERVATION_INVALID'):
                    self.build()
                self.assertNotIn('compile-gui-native', self.events)
                self.assertFalse(self.output.exists())

    def test_source_and_generated_identity_mutations_fail_closed(self):
        for relative in ('ui/tauri/src-tauri/tauri.conf.json', tool.release.PROVIDER):
            self.change = lambda stage: (stage / relative).write_bytes(b'changed')
            with self.subTest(relative=relative), self.assertRaises(tool.source.BuildError):
                self.build()
            self.assertFalse(self.output.exists())

    def test_producer_cannot_change_during_compile_or_outer_signing(self):
        self.change = lambda stage: (stage / 'resident-pipe').write_bytes(executable() + b'other')
        with self.assertRaisesRegex(tool.source.BuildError, 'SIGNED_PRODUCER_CHANGED'):
            self.build()
        self.assertFalse(self.output.exists())
        self.change = lambda stage: None
        self.outer_change = lambda app: (app / tool.layout.ASSEMBLY_IMAGES['producer']).write_bytes(executable() + b'other')
        with self.assertRaisesRegex(tool.source.BuildError, 'SIGNED_PRODUCER_CHANGED'):
            self.build()
        self.assertFalse(self.output.exists())

    def test_outer_signing_cannot_change_the_signed_native_image(self):
        self.outer_change = lambda app: (app / tool.layout.ASSEMBLY_IMAGES['native']).write_bytes(image('native') + b'replaced')
        with self.assertRaisesRegex(tool.source.BuildError, 'SIGNED_NATIVE_CHANGED'):
            self.build()
        self.assertFalse(self.output.exists())

    def test_final_signature_must_keep_the_bound_cdhash(self):
        original = self.observe
        def changed(*args, **kwargs):
            result = original(*args, **kwargs)
            if args[2] == 'producer' and kwargs.get('cdhash'):
                result['cdhash'] = 'a' * 40
            return result
        with patch.object(tool.release, 'observe', changed):
            with self.assertRaisesRegex(tool.source.BuildError, 'SIGNATURE_OBSERVATION_INVALID'):
                self.build()
        self.assertFalse(self.output.exists())

    def test_signer_failure_removes_only_new_output(self):
        with patch.object(tool.release, 'sign', side_effect=tool.source.BuildError('SIGN_FAILED')):
            with self.assertRaisesRegex(tool.source.BuildError, 'SIGN_FAILED'):
                self.build()
        self.assertFalse(self.output.exists())
        self.output.mkdir()
        (self.output / 'keep').write_text('original')
        with self.assertRaises(FileExistsError):
            self.build()
        self.assertEqual((self.output / 'keep').read_text(), 'original')

    def test_identity_source_and_host_are_explicit_not_discovered(self):
        for team, cert, revision in [('bad', self.cert, self.revision),
                                     (self.team, '-', self.revision),
                                     (self.team, self.cert, 'f' * 40)]:
            with self.assertRaises(tool.source.BuildError):
                tool.build(self.output, team, cert, revision)
        with patch.object(tool.platform, 'mac_ver', return_value=('26.0', (), '')):
            with self.assertRaisesRegex(tool.source.BuildError, 'SIGNED_RELEASE_HOST_REQUIRED'):
                self.build()
        self.assertEqual(self.events, [])

    def test_signed_inventory_is_separate_from_inert_candidate(self):
        self.build()
        app = self.output / tool.APP
        hashes = {r: tool.source.digest((app / p).read_bytes()) for r, p in tool.layout.ASSEMBLY_IMAGES.items()}
        with self.assertRaises(tool.layout.LayoutError):
            tool.layout.verify_assembly(app, 'darwin-arm64', hashes)
        for relative in ('Contents/unexpected', 'Contents/Frameworks/shadow.dylib', 'Contents/_CodeSignature/extra'):
            path = app / relative
            path.write_bytes(b'bad')
            with self.subTest(relative=relative), self.assertRaises(tool.source.BuildError):
                tool.release.inventory(app, hashes)
            path.unlink()

    def test_cli_refuses_imported_provider_manifest_producer_and_notarization(self):
        for option in ('--producer', '--provider', '--manifest', '--notarize', '--entitlements', '--flags'):
            stream = io.StringIO()
            with contextlib.redirect_stdout(stream):
                self.assertEqual(tool.main([option, 'PRIVATE']), 1)
            self.assertNotIn('PRIVATE', stream.getvalue())
        self.assertEqual(self.events, [])


class SignedCommandTests(unittest.TestCase):
    def test_sign_command_is_fixed_inside_out_without_credential_or_entitlement_options(self):
        calls = []
        with patch.object(tool.source, 'command', side_effect=lambda args, cwd, env: calls.append(args)):
            for role in tool.release.IDENTIFIERS:
                tool.release.sign(Path('/owned/code'), role, 'C' * 40, Path('/owned'), {})
        for role, args in zip(tool.release.IDENTIFIERS, calls):
            self.assertEqual(args, ['/usr/bin/codesign', '--force', '--sign', 'C' * 40,
                '--identifier', tool.release.IDENTIFIERS[role], '--options', 'runtime',
                '--timestamp', '--generate-entitlement-der', '/owned/code'])
            self.assertNotIn('--deep', args)
            self.assertNotIn('--entitlements', args)

    def test_release_compiler_selects_one_fixed_provider_and_no_fixture_define(self):
        with tempfile.TemporaryDirectory() as temporary:
            stage, calls = Path(temporary), []
            def command(args, cwd, env):
                calls.append(list(args))
                return ('\n'.join(tool.support.EXPORTS)).encode() if args[0] == '/usr/bin/nm' else b''
            with patch.object(tool.source, 'command', side_effect=command):
                tool.support.compile_images(stage, {}, '/swift', '/sdk', signed_release=True)
            swift = calls[3]
            self.assertIn('arm64-apple-macosx27.0', swift)
            self.assertEqual(swift.count(str(stage / tool.release.PROVIDER)), 1)
            self.assertEqual(swift.count('TETHER_SIGNED_RELEASE'), 1)
            self.assertFalse(any('FIXTURE' in value for value in swift))
            self.assertNotIn('--cfg', swift)

    def test_full_observation_is_required_and_numeric_boolean_is_rejected(self):
        with self.assertRaises(tool.source.BuildError):
            tool.release.validate_observation({'secureTimestamp': 1}, 'producer', 'FIXTURE123', 'C' * 40)


if __name__ == '__main__':
    unittest.main()

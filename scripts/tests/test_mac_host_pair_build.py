"""Synthetic helper-build metadata only; no compiler, signing or helper execution."""
import json
from pathlib import Path
from pathlib import PurePosixPath
import sys
import struct
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import build_mac_host_pair as tool


class HostPairBuildTests(unittest.TestCase):
    def config(self):
        return {'gateway': 'https://gateway.example', 'issuer': 'https://issuer.example',
                'clientID': 'public-client'}

    def test_unsigned_candidate_accepts_only_a_fully_empty_configuration(self):
        # An unconfigured candidate must keep GUI pairing unavailable without partial trust inputs.
        values = tool.configuration_from_env({}, required=False)
        self.assertEqual(values, {'gateway': '', 'issuer': '', 'clientID': ''})
        for env in ({'TETHER_PAIR_GATEWAY': 'https://gateway.example'},
                    {'TETHER_PAIR_ISSUER': 'https://issuer.example', 'TETHER_PAIR_CLIENT_ID': 'client'}):
            with self.subTest(env=env), self.assertRaisesRegex(tool.PairBuildError, 'PAIR_CONFIG_PARTIAL'):
                tool.configuration_from_env(env, required=False)

    def test_signed_release_requires_all_three_valid_configuration_values(self):
        # A signed product must never silently contain an unavailable or partial pairing client.
        with self.assertRaisesRegex(tool.PairBuildError, 'PAIR_CONFIG_REQUIRED'):
            tool.configuration_from_env({}, required=True)
        env = {'TETHER_PAIR_GATEWAY': 'https://gateway.example',
               'TETHER_PAIR_ISSUER': 'https://issuer.example', 'TETHER_PAIR_CLIENT_ID': 'public-client'}
        self.assertEqual(tool.configuration_from_env(env, required=True), self.config())

    def test_gateway_issuer_and_client_id_formats_are_checked(self):
        # Invalid endpoint forms or nonvisible client IDs must be rejected before compilation.
        valid = self.config()
        cases = [dict(valid, gateway='https://GATEWAY.example'),
                 dict(valid, gateway='https://gateway.example/path'),
                 dict(valid, gateway='https://user@gateway.example'),
                 dict(valid, gateway='https://gateway.example?query=1'),
                 dict(valid, issuer='http://issuer.example'),
                 dict(valid, issuer='HTTPS://issuer.example'),
                 dict(valid, issuer='https:issuer.example'),
                 dict(valid, issuer='https://issuer.example?query=1'),
                 dict(valid, issuer='https://user@issuer.example'),
                 dict(valid, issuer='https://issuer.example:bad'),
                 dict(valid, issuer='https://issuer%ZZ.example'),
                 dict(valid, issuer='https://issuer.example/path with space'),
                 dict(valid, clientID='client\n')]
        for value in cases:
            with self.subTest(value=value), self.assertRaises(tool.PairBuildError):
                tool.validate_configuration(value, required=True)

    def test_issuer_port_matches_the_helpers_numeric_port_rule(self):
        # Go's URL parser requires numeric port syntax but does not impose Python's 65535 limit.
        value = dict(self.config(), issuer='https://issuer.example:999999')
        self.assertEqual(tool.validate_configuration(value, required=True), value)

    def test_ldflags_and_go_version_record_must_match_exactly(self):
        # The app record cannot claim values that the executable's Go build info does not contain.
        values = self.config()
        flags = tool.linker_flags(values)
        record = {'pairConfig': values, 'ldflags': flags,
                  'goBuildInfo': {'path': tool.ROLE, 'ldflags': flags}}
        output = f'executable: go1.27.3\n\tpath\t{tool.ROLE}\n\tbuild\t-ldflags={json.dumps(flags)}\n'
        tool.verify_build_info(output, record)
        with self.assertRaisesRegex(tool.PairBuildError, 'PAIR_BUILD_INFO_MISMATCH'):
            tool.verify_build_info(output.replace('gateway.example', 'other.example'), record)
        changed_record = dict(record, ldflags=flags + ' altered')
        with self.assertRaisesRegex(tool.PairBuildError, 'PAIR_BUILD_INFO_MISMATCH'):
            tool.verify_build_info(output, changed_record)

    def test_client_configuration_values_are_quote_safe_in_go_ldflags(self):
        # Valid visible OAuth client IDs may contain quotes and backslashes; Go must receive them literally.
        values = dict(self.config(), clientID='public"\\client')
        encoded = tool.linker_flags(values)
        self.assertIn('public\\"\\\\client', encoded)


class HostPairBuildCommandTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        self.output = self.root / 'output'
        self.revision, self.tree = 'a' * 40, 'b' * 40
        self.config = {'gateway': 'https://gateway.example', 'issuer': 'https://issuer.example',
                       'clientID': 'public-client'}
        self.ldflags = tool.linker_flags(self.config)
        self.image = (struct.pack('<8I', 0xfeedfacf, 0x100000c, 0, 2, 1, 24, 0, 0)
                      + struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0))
        self.calls = []
        self.patch(tool.source, 'ROOT', self.repo)
        self.patch(tool.source, 'head', lambda: (self.revision, self.tree))
        self.patch(tool.source, 'snapshot', self.snapshot)
        self.patch(tool.source, 'build_metadata', lambda *args: {
            'GOVERSION': 'go1.27.3', 'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '1'})
        self.patch(tool.source, 'source_inventory', lambda *args: None)
        self.patch(tool.source, 'closure', lambda module, env, role: ([role], []))
        self.patch(tool.source, 'module_metadata', lambda *args: [])
        self.patch(tool.source, 'command', self.command)
        self.patch(tool.platform, 'system', lambda: 'Darwin')
        self.patch(tool.platform, 'machine', lambda: 'arm64')

    def patch(self, owner, name, value):
        item = patch.object(owner, name, value)
        item.start()
        self.addCleanup(item.stop)

    def snapshot(self, revision, stage, recipe_paths):
        self.assertEqual(revision, self.revision)
        files = {}
        for package, names in tool.product_files_for('darwin', True).items():
            suffix = package[len(tool.source.PROJECT):].lstrip('/')
            for name in names:
                relative = str(PurePosixPath('backend') / suffix / name)
                path = stage / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'package fixture\n')
                files[relative] = tool.source.digest(path.read_bytes())
        locks = {}
        for name in ('go.mod', 'go.sum'):
            path = stage / 'backend' / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b'fixture lock\n')
            locks['backend/' + name] = tool.source.digest(path.read_bytes())
        return files, locks, {name: 'c' * 64 for name in recipe_paths}

    def command(self, args, cwd, env):
        self.calls.append(args)
        if args[:2] == ['go', 'build']:
            self.assertNotIn('-tags', args)
            self.assertEqual(args[args.index('-ldflags') + 1], self.ldflags)
            path = Path(args[args.index('-o') + 1])
            path.write_bytes(self.image)
            return b''
        self.assertEqual(args[:3], ['go', 'version', '-m'])
        return (f"{args[-1]}: go1.27.3\n\tpath\t{tool.ROLE}\n"
                f'\tbuild\t-ldflags={json.dumps(self.ldflags)}\n').encode()

    def test_build_records_production_image_and_verified_linker_values(self):
        # The bundled binary must be the production Go role with exact environment-derived linker values.
        record = tool.build(self.output, self.config)
        tool.validate_record(record, self.revision, self.tree)
        self.assertEqual(record['pairConfig'], self.config)
        self.assertEqual(record['ldflags'], self.ldflags)
        self.assertEqual(record['goBuildInfo'], {'path': tool.ROLE, 'ldflags': self.ldflags})
        self.assertEqual(len(self.calls), 2)
        self.assertEqual(set(path.name for path in self.output.iterdir()), {'tether-host-pair', 'BUILD_RECORD.json'})


if __name__ == '__main__':
    unittest.main()

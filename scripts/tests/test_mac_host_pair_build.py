"""Helper build records and a small real trimmed Go embed binary."""
import contextlib
import io
import json
import os
import platform
from pathlib import Path
from pathlib import PurePosixPath
import sys
import subprocess
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

    def test_shared_value_table_is_accepted_and_rejected_as_listed(self):
        # Why: the builder must accept only a subset of what the Go helper accepts, or a signed
        # release would embed a helper that always reports unavailable. The accepted rows are also
        # checked by Go (pair_config_values_test.go) against validGatewayForm and ValidClient.
        table = json.loads((tool.source.ROOT / 'backend/internal/hostpairoauth/testdata/'
                            'pair_config_values.json').read_text())
        self.assertTrue(all(table['accepted'].values()))
        self.assertTrue(all(table['rejected'].values()))
        ok_issuer, ok_client = self.config()['issuer'], self.config()['clientID']
        for gateway in table['accepted']['gateway']:
            with self.subTest(gateway=gateway):
                self.assertTrue(tool.valid_gateway(gateway))
        for issuer in table['accepted']['issuer']:
            with self.subTest(issuer=issuer):
                self.assertTrue(tool.valid_client(issuer, ok_client))
        for client_id in table['accepted']['clientID']:
            with self.subTest(clientID=client_id):
                self.assertTrue(tool.valid_client(ok_issuer, client_id))
        for gateway in table['rejected']['gateway']:
            with self.subTest(gateway=gateway):
                self.assertFalse(tool.valid_gateway(gateway))
        for issuer in table['rejected']['issuer']:
            with self.subTest(issuer=issuer):
                self.assertFalse(tool.valid_client(issuer, ok_client))
        for client_id in table['rejected']['clientID']:
            with self.subTest(clientID=client_id):
                self.assertFalse(tool.valid_client(ok_issuer, client_id))

    def test_pair_config_json_has_fixed_keys_order_and_newline(self):
        # Why: one canonical encoding keeps the staged input, record hash, and verifier expectation identical.
        values = self.config()
        self.assertEqual(tool.pair_config_bytes(values),
                         b'{"gateway":"https://gateway.example","issuer":"https://issuer.example","clientID":"public-client"}\n')

    def test_repository_pair_config_is_the_canonical_empty_json(self):
        # Why: committed helper source must remain unavailable until the approved build supplies all values.
        empty = {'gateway': '', 'issuer': '', 'clientID': ''}
        path = tool.source.ROOT / tool.PAIR_CONFIG_PATH
        self.assertEqual(path.read_bytes(), tool.pair_config_bytes(empty))

    @unittest.skipUnless(platform.system() == 'Darwin' and platform.machine() == 'arm64',
                         'The real trimmed binary regression requires macOS ARM64.')
    def test_real_trimpath_binary_embeds_and_verifies_pair_configuration(self):
        # Why: an actual trimmed Go binary proves exact embedded bytes and recorded values.
        values = dict(self.config(), clientID='public-client_1.~')
        config_bytes = tool.pair_config_bytes(values)
        with tempfile.TemporaryDirectory(dir=Path.cwd()) as temporary:
            root = Path(temporary)
            (root / 'go.mod').write_text('module ' + tool.ROLE + '\n\ngo 1.23\n')
            (root / 'pair_config.json').write_bytes(config_bytes)
            (root / 'main.go').write_text(
                'package main\nimport ("os"; _ "embed")\n'
                '//go:embed pair_config.json\nvar pairConfig []byte\n'
                'func main() { _, _ = os.Stdout.Write(pairConfig) }\n')
            binary = root / 'pair-regression'
            subprocess.run(['go', 'build', *tool.FLAGS, '-o', str(binary), '.'],
                           cwd=root, check=True, capture_output=True, text=True,
                           env={**os.environ, 'GOWORK': 'off', 'GOFLAGS': '',
                                'GOTOOLCHAIN': 'local', 'GOPROXY': 'off', 'GOSUMDB': 'off',
                                'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '0'})
            run = subprocess.run([str(binary)], check=True, capture_output=True)
            self.assertEqual(run.stdout, config_bytes)
            record = {'pairConfig': values, 'pairConfigSha256': tool.source.digest(config_bytes),
                      'image': tool.inspect_image(binary)}
            tool.verify_binary(binary, record)
            other = dict(values, clientID=values['clientID'] + 'x')
            changed = {'pairConfig': other,
                       'pairConfigSha256': tool.source.digest(tool.pair_config_bytes(other)),
                       'image': record['image']}
            with self.assertRaisesRegex(tool.PairBuildError, 'PAIR_CONFIG_BINARY_MISMATCH'):
                tool.verify_binary(binary, changed)


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
        self.config_bytes = tool.pair_config_bytes(self.config)
        self.mutate_config_after_build = False
        self.image = (struct.pack('<8I', 0xfeedfacf, 0x100000c, 0, 2, 1, 24, 0, 0)
                      + struct.pack('<6I', 0x32, 24, 1, 27 << 16, 27 << 16, 0))
        self.calls = []
        self.patch(tool.source, 'ROOT', self.repo)
        self.patch(tool.source, 'head', lambda: (self.revision, self.tree))
        self.patch(tool.source, 'snapshot', self.snapshot)
        self.patch(tool.source, 'source_blob', self.source_blob)
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

    def source_blob(self, revision, relative):
        self.assertEqual(revision, self.revision)
        self.assertEqual(relative, tool.PAIR_CONFIG_PATH)
        return tool.pair_config_bytes({'gateway': '', 'issuer': '', 'clientID': ''})

    def command(self, args, cwd, env):
        self.calls.append(args)
        if args[:2] == ['go', 'build']:
            self.assertNotIn('-tags', args)
            self.assertNotIn('-ldflags', args)
            self.assertEqual(args[2:2 + len(tool.FLAGS)], list(tool.FLAGS))
            staged_config = cwd / 'cmd/tether-host-pair/pair_config.json'
            self.assertEqual(staged_config.read_bytes(), self.config_bytes)
            path = Path(args[args.index('-o') + 1])
            path.write_bytes(self.image + self.config_bytes)
            if self.mutate_config_after_build:
                staged_config.write_bytes(self.config_bytes + b'changed')
            return b''
        self.fail(f'unexpected Go command: {args}')

    def test_build_records_pair_config_hash_and_embedded_image_bytes(self):
        # Why: the builder must write only the staged config, omit linker flags, and verify embedded bytes.
        record = tool.build(self.output, self.config)
        tool.validate_record(record, self.revision, self.tree)
        self.assertEqual(record['pairConfig'], self.config)
        self.assertEqual(record['pairConfigSha256'], tool.source.digest(self.config_bytes))
        self.assertNotIn('ldflags', record)
        self.assertNotIn('goBuildInfo', record)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(set(path.name for path in self.output.iterdir()), {'tether-host-pair', 'BUILD_RECORD.json'})

    def test_record_rejects_a_pair_config_hash_that_does_not_match_its_values(self):
        # Why: a self-declared pairConfig must not authenticate a different canonical JSON hash.
        record = tool.build(self.output, self.config)
        changed = dict(record, pairConfigSha256='0' * 64)
        with self.assertRaisesRegex(tool.PairBuildError, 'PAIR_CONFIG_HASH_MISMATCH'):
            tool.validate_record(changed, self.revision, self.tree)

    def test_build_rechecks_the_rewritten_pair_config_file(self):
        # Why: pair_config.json is the sole intentional snapshot exception and still needs a post-build hash check.
        self.mutate_config_after_build = True
        with self.assertRaisesRegex(tool.source.BuildError, 'PAIR_CONFIG_CHANGED'):
            tool.build(self.output, self.config)
        self.assertFalse(self.output.exists())

    def test_main_reports_blocked_build_with_exit_two_and_summary_json(self):
        # Why: CLI callers must distinguish an inert recorded build from failure by its exit code and JSON.
        record = {'sourceCommit': self.revision, 'image': {'sha256': 'c' * 64}}
        stream = io.StringIO()
        with patch.object(tool, 'build', return_value=record), contextlib.redirect_stdout(stream):
            self.assertEqual(tool.main(['--output', str(self.output)]), 2)
        self.assertEqual(json.loads(stream.getvalue()), {
            'status': 'blocked', 'code': 'HOST_PAIR_BUILD_RECORDED',
            'record': 'BUILD_RECORD.json', 'sourceCommit': self.revision,
            'imageSha256': 'c' * 64, 'blockers': list(tool.BLOCKERS),
        })

    def test_main_reports_build_failure_with_exit_one_and_error_code(self):
        # Why: rejected inputs must stay machine-readable and must never look like a recorded candidate.
        stream = io.StringIO()
        with patch.object(tool, 'build', side_effect=tool.PairBuildError('PAIR_CONFIG_PARTIAL')), \
                contextlib.redirect_stdout(stream):
            self.assertEqual(tool.main(['--output', str(self.output)]), 1)
        self.assertEqual(json.loads(stream.getvalue()), {
            'status': 'blocked', 'code': 'PAIR_CONFIG_PARTIAL',
        })


if __name__ == '__main__':
    unittest.main()

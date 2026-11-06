import copy
import importlib.util
from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('library_ci', SCRIPTS / 'check_mac_library_ci.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class BlockedLibraryCIContractTests(unittest.TestCase):
    def setUp(self):
        self.head, self.tree = 'a' * 40, 'b' * 40
        self.record = {'schema': 'rescue-mac-resident-library-build/1',
                       'status': 'blocked', 'activation': 'blocked', 'distribution': 'blocked',
                       'sourceCommit': self.head, 'sourceTree': self.tree,
                       'settings': {'GOVERSION': 'go1.27.3', 'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '1'},
                       'producerExecutable': False, 'cryptographicAttestation': False,
                       'archive': {'path': 'residentpipe-darwin-arm64.a', 'sha256': 'c' * 64},
                       'blockers': list(tool.builder.BLOCKERS)}
        self.summary = {'status': 'blocked', 'code': 'LIBRARY_BUILD_RECORDED', 'record': 'BUILD_RECORD.json',
                        'sourceCommit': self.head, 'archiveSha256': 'c' * 64, 'blockers': list(tool.builder.BLOCKERS)}

    def test_documented_exit_two_remains_blocked(self):
        tool.validate_result(2, self.summary, self.record, self.head, self.tree, 'arm64')

    def test_zero_failure_and_wrong_summary_are_never_build_success(self):
        for status in (0, 1, -1, 3):
            with self.subTest(status=status), self.assertRaises(ValueError):
                tool.validate_result(status, self.summary, self.record, self.head, self.tree, 'arm64')
        bad = dict(self.summary, code='BUILD_INPUT_INVALID')
        with self.assertRaises(ValueError):
            tool.validate_result(2, bad, self.record, self.head, self.tree, 'arm64')

    def test_activation_wrong_revision_arch_or_artifact_are_rejected(self):
        for field, value in [('activation', 'enabled'), ('distribution', 'enabled'),
                             ('producerExecutable', True), ('cryptographicAttestation', True),
                             ('sourceCommit', 'd' * 40), ('sourceTree', 'd' * 40),
                             ('blockers', []), ('archive', {'path': '../other', 'sha256': 'c' * 64})]:
            record = copy.deepcopy(self.record)
            record[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                tool.validate_result(2, self.summary, record, self.head, self.tree, 'arm64')
        with self.assertRaises(ValueError):
            tool.validate_result(2, self.summary, self.record, self.head, self.tree, 'amd64')

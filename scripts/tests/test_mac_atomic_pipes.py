import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location('atomic_pipes', SCRIPTS / 'check_mac_atomic_pipes.py')
tool = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(tool)
sys.path.pop(0)


class AtomicPipeTests(unittest.TestCase):
    def report(self, mode):
        passed, allocations, closed, children = tool.CASES[mode]
        return f'NATIVE_ATOMIC_PIPE mode={mode} passed={passed} failed=0 allocations={allocations} closed={closed} open=0 childrenStarted={children} childrenWaited={children} producer=0\n'

    def test_all_exact_reports(self):
        for mode, expected in tool.CASES.items():
            self.assertEqual(tool.validate_report(mode, self.report(mode)), expected[0])

    def test_incomplete_or_failed_reports_reject(self):
        for mode in tool.CASES:
            report = self.report(mode)
            for output in ['', report + report, report + 'extra', report.replace('failed=0', 'failed=1'),
                           report.replace('open=0', 'open=1'), report.replace('producer=0', 'producer=1'),
                           report.replace('passed=', 'unknown='), report.replace(f'mode={mode}', 'mode=other'),
                           report.replace(f'closed={tool.CASES[mode][2]}', 'closed=99'),
                           report.replace(f'allocations={tool.CASES[mode][1]}', 'allocations=99')]:
                with self.subTest(mode=mode, output=output), self.assertRaises(ValueError):
                    tool.validate_report(mode, output)
        report = self.report('concurrent')
        for field in ['childrenStarted', 'childrenWaited']:
            with self.assertRaises(ValueError):
                tool.validate_report('concurrent', report.replace(f'{field}=16', f'{field}=15'))

    def test_runtime_and_sdk_version_gate(self):
        for version in ['27.0', '27.0.1', '27.2', '28.0']:
            tool.require_version(version)
        for version in ['', '26.6.2', '14.4', '27', '27.0-beta', '27.0\n', '27.-1']:
            with self.subTest(version=version), self.assertRaises(ValueError):
                tool.require_version(version)

    def test_source_allocation_contract(self):
        source = (tool.NATIVE / 'OwnedPreparationIO.swift').read_text(encoding='utf-8')
        tool.validate_source(source)
        for mutated in [source.replace('Darwin.pipe2(&pair, O_CLOEXEC)', 'pipe(&pair)'),
                        source.replace('Darwin.pipe2(&pair, O_CLOEXEC)', 'Darwin.pipe2(&pair, 0)'),
                        source.replace('#available(macOS 27.0, *)', '#available(macOS 14.4, *)'),
                        source + '\n// F_SETFD fallback', source + '\n// dlsym fallback']:
            with self.assertRaises((ValueError, IndexError)):
                tool.validate_source(mutated)

    def test_non_mac_refuses_without_tools(self):
        with patch.object(tool.sys, 'platform', 'linux'), patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()

    def test_old_mac_refuses_before_compiler_or_allocation(self):
        with patch.object(tool.sys, 'argv', ['checker']), patch.object(tool.sys, 'platform', 'darwin'), \
                patch.object(tool.platform, 'machine', return_value='arm64'), \
                patch.object(tool.platform, 'mac_ver', return_value=('26.6.2', (), '')), \
                patch.object(tool, 'command') as start:
            with self.assertRaises(ValueError):
                tool.run()
        start.assert_not_called()

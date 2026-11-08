import importlib.util
from pathlib import Path
import sys
from types import SimpleNamespace
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
        return f'NATIVE_ATOMIC_PIPE mode={mode} passed={passed} failed=0 allocations={allocations} closed={closed} open=0 childrenStarted={children} childrenWaited={children} threadsJoined={children} inheritanceExits=0 producer=0\n'

    def test_all_exact_reports(self):
        for mode, expected in tool.CASES.items():
            self.assertEqual(tool.validate_report(mode, self.report(mode)), expected[0])

    def test_incomplete_or_failed_reports_reject(self):
        for mode in tool.CASES:
            report = self.report(mode)
            for output in ['', report + report, report + 'extra', report.replace('failed=0', 'failed=1'),
                           report.replace('open=0', 'open=1'), report.replace('producer=0', 'producer=1'),
                           report.replace('inheritanceExits=0', 'inheritanceExits=1'),
                           report.replace('passed=', 'unknown='), report.replace(f'mode={mode}', 'mode=other'),
                           report.replace(f'closed={tool.CASES[mode][2]}', 'closed=99'),
                           report.replace(f'allocations={tool.CASES[mode][1]}', 'allocations=99')]:
                with self.subTest(mode=mode, output=output), self.assertRaises(ValueError):
                    tool.validate_report(mode, output)
        report = self.report('concurrent')
        for field in ['childrenStarted', 'childrenWaited', 'threadsJoined']:
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


    def test_temporary_legacy_mutation_restores_old_gap(self):
        source = (tool.NATIVE / 'OwnedPreparationIO.swift').read_text(encoding='utf-8')
        mutated = tool.legacy_mutant(source)
        self.assertIn('Darwin.pipe(&pair)', mutated)
        self.assertNotIn('Darwin.pipe2', mutated)
        self.assertIn('fcntl(end.fd, F_SETFD, flags | FD_CLOEXEC)', mutated)
        self.assertLess(mutated.index('try adoptPipe()\n        for end'), mutated.index('fcntl(end.fd, F_SETFD'))
        self.assertIn('try fixtureAllocationProbe?(pair)', mutated)
        with self.assertRaises(ValueError):
            tool.validate_source(mutated)
        with self.assertRaises(ValueError):
            tool.legacy_mutant(source.replace('// Parent write/read only.', '// anchor changed'))
        self.assertEqual((tool.NATIVE / 'OwnedPreparationIO.swift').read_text(encoding='utf-8'), source)

    def test_executed_success_and_sanitized_failure_diagnostics(self):
        result = SimpleNamespace(returncode=0, stdout=self.report('concurrent'), stderr='')
        progress = {}
        with patch.object(tool, 'completed', return_value=result):
            self.assertEqual(tool.execute_mode('fixture', 'concurrent', progress), 65)
        result = SimpleNamespace(returncode=1, stdout='', stderr='private/path\nNATIVE_ATOMIC_PIPE_CONDITION 2\nNATIVE_ATOMIC_PIPE_CHILD_EXIT 89\n')
        with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
            tool.execute_mode('fixture', 'concurrent', progress)
        self.assertEqual(progress, {'phase': 'fixture-execute', 'mode': 'concurrent', 'returncode': 1, 'condition': 2, 'child_exit': 89})
        self.assertNotIn('private', str(progress))

    def test_malformed_or_unbounded_diagnostics_are_not_retained(self):
        for diagnostic in ['NATIVE_ATOMIC_PIPE_CONDITION 66\nNATIVE_ATOMIC_PIPE_CHILD_EXIT 256\n',
                           'NATIVE_ATOMIC_PIPE_CONDITION path\nNATIVE_ATOMIC_PIPE_CHILD_EXIT -1\n',
                           'NATIVE_ATOMIC_PIPE_CONDITION 2\nNATIVE_ATOMIC_PIPE_CONDITION 3\n']:
            result = SimpleNamespace(returncode=1, stdout='', stderr=diagnostic)
            progress = {}
            with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
                tool.execute_mode('fixture', 'concurrent', progress)
            self.assertIsNone(progress['condition'])
            self.assertIsNone(progress['child_exit'])

    def test_only_expected_executed_and_collected_mutant_counts_as_red(self):
        output = self.report('concurrent').replace('passed=65 failed=0', 'passed=33 failed=32').replace('inheritanceExits=0', 'inheritanceExits=16')
        diagnostics = 'NATIVE_ATOMIC_PIPE_CONDITION 1\nNATIVE_ATOMIC_PIPE_CHILD_EXIT 89\n'
        result = SimpleNamespace(returncode=1, stdout=output, stderr=diagnostics)
        with patch.object(tool, 'completed', return_value=result):
            self.assertEqual(tool.execute_mode('fixture', 'concurrent', {}, mutant=True), 32)
        failures = [SimpleNamespace(returncode=code, stdout=output, stderr=diagnostics) for code in [0, 2, -11]]
        failures += [SimpleNamespace(returncode=1, stdout=bad, stderr=diagnostics) for bad in [
            '', output + output, output.replace('childrenWaited=16', 'childrenWaited=15'),
            output.replace('threadsJoined=16', 'threadsJoined=15'), output.replace('open=0', 'open=1'),
            output.replace('closed=32', 'closed=31'), output.replace('failed=32', 'failed=31')]]
        failures += [SimpleNamespace(returncode=1, stdout=output, stderr=bad) for bad in [
            'compiler failed', '', diagnostics.replace('CONDITION 1', 'CONDITION 2'), diagnostics.replace('EXIT 89', 'EXIT 78')]]
        for result in failures:
            with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
                tool.execute_mode('fixture', 'concurrent', {}, mutant=True)

    def test_build_failure_cannot_count_as_expected_native_red(self):
        progress = {'phase': 'mutant-build', 'mode': None}
        result = SimpleNamespace(returncode=1, stdout=self.report('concurrent'), stderr='private compiler/path')
        with patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
            tool.command(['compiler'], {}, progress)
        self.assertEqual(progress, {'phase': 'mutant-build', 'mode': None, 'returncode': 1})


    def test_mixed_child_failures_cannot_pass_as_inheritance_red(self):
        # First child really reports inheritance; later children may instead
        # fail their sentinel, arguments, fd scan, or terminate by signal.
        # Total failed checks / waits / joins / closes are unchanged.
        base = self.report('concurrent').replace('passed=65 failed=0', 'passed=33 failed=32')
        diagnostics = 'NATIVE_ATOMIC_PIPE_CONDITION 1\nNATIVE_ATOMIC_PIPE_CHILD_EXIT 89\n'
        for inherited in [0, 1, 15, 17]:
            result = SimpleNamespace(returncode=1,
                                     stdout=base.replace('inheritanceExits=0', f'inheritanceExits={inherited}'),
                                     stderr=diagnostics)
            with self.subTest(inherited=inherited), patch.object(tool, 'completed', return_value=result), self.assertRaises(ValueError):
                tool.execute_mode('fixture', 'concurrent', {}, mutant=True)

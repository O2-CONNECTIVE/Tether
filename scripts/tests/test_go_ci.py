"""Synthetic event-stream checks for CI reporting, not product acceptance."""

import contextlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from go_ci import test_events
import go_ci


class GoEventTests(unittest.TestCase):
    def test_accepted_transport_is_explicitly_in_product_inventory(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertEqual(go_ci.product_files_for(target)[package],
                                 {'activity.go', 'session.go', 'transport.go'})

    def test_status_and_cleanup_cgo_inventory_is_exact(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        for target in ('linux', 'darwin', 'windows'):
            for enabled in (False, True):
                with self.subTest(target=target, cgo=enabled):
                    inv = go_ci.product_files_for(target, enabled)
                    self.assertEqual(inv[prefix + 'cmd/rescue-mac-agent'], {
                        'main.go', 'config.go', 'status.go',
                        'status_darwin.go' if target == 'darwin' else 'status_other.go',
                        'status_clock_darwin.go' if target == 'darwin' and enabled else 'status_clock_other.go'})
                    group = {x for x in inv[prefix + 'internal/macagent'] if x.startswith('groupobserver')}
                    self.assertEqual(group, {'groupobserver.go'} | (
                        {'groupobserver_darwin.go' if enabled else 'groupobserver_darwin_nocgo.go'} if target == 'darwin' else set()))
                    self.assertIn('local_summary.go', inv[prefix + 'internal/agentexec'])
                    if target == 'darwin':
                        self.assertIn('native_darwin.go' if enabled else 'native_other.go', inv[prefix + 'internal/agentkeys'])
        for invalid in (None, 0, 1, '1'):
            with self.assertRaises(ValueError):
                go_ci.product_files_for('darwin', invalid)

    def events(self):
        module = 'github.com/O2-CONNECTIVE/rescue-host/backend'
        result = [
            {'Package': module, 'Test': 'TestPublicBoundary', 'Action': 'run'},
            {'Package': module, 'Test': 'TestPublicBoundary', 'Action': 'pass'},
            {'Package': module, 'Action': 'pass'},
        ]
        for suffix in ('internal/auth', 'internal/control', 'internal/forward',
                       'internal/lease', 'internal/stdio', 'cmd/rescue-backend', 'supervisor', 'servicehost',
                       'supervisor/ephemeral', 'internal/localcontrol',
                       'internal/runbackend', 'internal/childjob',
                       'internal/protectedkey', 'internal/bootstrap',
                       'internal/bootstrapio', 'internal/bootstrapauth', 'internal/childstdio', 'internal/serviceready',
                       'internal/serviceprovision', 'cmd/rescue-service-host', 'cmd/rescue-auth-helper', 'internal/servicecontrol', 'internal/operatorauth', 'internal/serviceauthowner', 'internal/agentexec', 'internal/guestaccount',
                       'internal/agentchannel', 'internal/agentwire', 'internal/mcpadapter', 'cmd/rescue-mcp', 'internal/macagent',
                       'internal/agentlink', 'internal/maclaunch', 'internal/agentgateway', 'cmd/rescue-gateway', 'cmd/tether-echo-probe', 'cmd/tether-access-identity', 'cmd/tether-host-standby', 'internal/agentkeys', 'internal/agenttransport', 'cmd/rescue-mac-agent'):
            result.append({'Package': module + '/' + suffix, 'Action': 'skip'})
        return result

    def check(self, events, code=0):
        with contextlib.redirect_stdout(io.StringIO()):
            return test_events('\n'.join(json.dumps(event) for event in events), code)

    def test_one_passing_test_is_counted(self):
        self.assertEqual(self.check(self.events()), {'pass': 1, 'skip': 0, 'fail': 0})

    def test_empty_stream_is_rejected(self):
        with self.assertRaises(ValueError):
            self.check([])

    def test_package_without_tests_is_not_counted_as_pass(self):
        with self.assertRaises(ValueError):
            self.check([event for event in self.events() if 'Test' not in event])

    def test_all_skipped_tests_are_rejected(self):
        events = self.events()
        events[1]['Action'] = 'skip'
        with self.assertRaises(ValueError):
            self.check(events)

    def test_go_exit_failure_overrides_passing_events(self):
        with self.assertRaises(ValueError):
            self.check(self.events(), 1)

    def test_failed_subtest_is_not_hidden_by_parent_pass(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestPublicBoundary/child', 'Action': 'fail'})
        with self.assertRaises(ValueError):
            self.check(events)

    def test_unfinished_test_is_rejected(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestUnfinished', 'Action': 'run'})
        with self.assertRaises(ValueError):
            self.check(events)

    def test_missing_package_is_rejected(self):
        with self.assertRaises(ValueError):
            self.check(self.events()[:-1])

    def test_each_service_provision_package_result_is_required(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        for suffix in ('internal/serviceprovision', 'cmd/rescue-service-host', 'cmd/rescue-auth-helper'):
            with self.subTest(package=suffix), self.assertRaisesRegex(ValueError, 'Missing package test result'):
                self.check([event for event in self.events() if event['Package'] != prefix + suffix])

    def test_guest_package_result_is_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount'
        with self.assertRaisesRegex(ValueError, 'Missing package test result'):
            self.check([event for event in self.events() if event['Package'] != package])

    def test_broken_json_is_rejected(self):
        with self.assertRaises(ValueError), contextlib.redirect_stdout(io.StringIO()):
            test_events('{', 0)

    def test_observed_cpu_matches_requested_lane(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestPublicBoundary',
                       'Action': 'output', 'Output': 'observed_test_GOMAXPROCS=2\n'})
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(test_events('\n'.join(map(json.dumps, events)), 0, expected_cpu='2')['pass'], 1)
            with self.assertRaises(ValueError):
                test_events('\n'.join(map(json.dumps, events)), 0, expected_cpu='1')

    def test_missing_public_test_is_rejected(self):
        expected = {(self.events()[0]['Package'], 'TestPublicBoundary'),
                    (self.events()[0]['Package'], 'TestRequiredBoundary')}
        with self.assertRaises(ValueError), contextlib.redirect_stdout(io.StringIO()):
            test_events('\n'.join(map(json.dumps, self.events())), 0, expected_tests=expected)

    def test_new_skip_is_rejected_even_with_another_pass(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestOtherBoundary', 'Action': 'skip'})
        with self.assertRaises(ValueError):
            self.check(events)


class GoResourceRecordTests(unittest.TestCase):
    COUNTERS = ('test_exit_code', 'remaining_listeners',
                'remaining_connections', 'forbidden_dial_attempts')

    def record(self):
        return {**dict.fromkeys(self.COUNTERS, 0), 'duration_ms': 1,
                'listener_addresses': [], 'dial_addresses': []}

    def check(self, record):
        with tempfile.TemporaryDirectory() as directory:
            module = Path(directory)
            path = module / 'go-result.json'
            path.write_text(json.dumps(record), encoding='utf-8')
            try:
                with patch.object(go_ci, 'MODULE', module), contextlib.redirect_stdout(io.StringIO()):
                    go_ci.check_network_record()
            finally:
                self.assertFalse(path.exists(), 'Synthetic resource record must be removed')

    def test_integer_zero_counters_are_accepted(self):
        self.check(self.record())

    def test_noninteger_counters_are_rejected(self):
        for key in self.COUNTERS:
            for value in (False, True, 0.0, None, '0'):
                with self.subTest(key=key, value=value):
                    record = self.record()
                    record[key] = value
                    with self.assertRaises(ValueError):
                        self.check(record)

    def test_missing_counters_are_rejected(self):
        for key in self.COUNTERS:
            with self.subTest(key=key):
                record = self.record()
                del record[key]
                with self.assertRaises(ValueError):
                    self.check(record)

    def test_nonzero_counters_are_rejected(self):
        for key in self.COUNTERS:
            for value in (-1, 1):
                with self.subTest(key=key, value=value):
                    record = self.record()
                    record[key] = value
                    with self.assertRaises(ValueError):
                        self.check(record)


class GoProductionInventoryTests(unittest.TestCase):
    def test_concrete_auth_owner_is_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn(package, go_ci.product_files_for(target))
                self.assertEqual(go_ci.product_files_for(target)[package], {'owner.go'})
                for fault in ('missing-package', 'missing-file', 'unknown-file'):
                    actual = go_ci.product_files_for(target)
                    if fault == 'missing-package':
                        del actual[package]
                    elif fault == 'missing-file':
                        actual[package].remove('owner.go')
                    else:
                        actual[package].add('owner_unreviewed.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_agent_command_core_is_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec'
        files = {'contract.go', 'output.go', 'run.go', 'session.go', 'local_summary.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertEqual(go_ci.product_files_for(target)[package], files)
                for fault in ('missing-package', 'missing-file', 'unknown-file', 'native-file'):
                    actual = go_ci.product_files_for(target)
                    if fault == 'missing-package':
                        del actual[package]
                    elif fault == 'missing-file':
                        actual[package].remove('run.go')
                    elif fault == 'unknown-file':
                        actual[package].add('fake_runtime.go')
                    else:
                        actual[package].add('runtime_windows.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_agent_adapter_and_mac_runtime_are_required_and_limited(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        common = {
            'internal/agentchannel': {'channel.go', 'server.go', 'client.go', 'conn.go', 'keys.go', 'liveness.go'},
            'internal/agentwire': {'client.go', 'server.go', 'strict.go', 'wire.go'},
            'internal/agentlink': {'host.go', 'client.go'},
            'internal/mcpadapter': {'jsonrpc.go', 'server.go', 'tools.go', 'http_bridge.go'},
            'internal/agentgateway': {'gateway.go', 'http.go', 'token.go', 'config.go'},
            'internal/agenttransport': {'protocol.go', 'stream.go', 'gateway.go', 'mac.go'},
            'cmd/rescue-gateway': {'main.go', 'config.go'},
            'cmd/tether-echo-probe': {'main.go', 'probe.go'},
            'cmd/tether-access-identity': {'main.go'},
            'cmd/tether-host-standby': {'main.go'},
                        'cmd/rescue-mcp': {'main.go'},
        }
        mac = {'linux': 'native_other.go', 'darwin': 'native_darwin.go', 'windows': 'native_other.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                for suffix, files in common.items():
                    self.assertEqual(inventory[prefix + suffix], files)
                self.assertEqual(inventory[prefix + 'internal/macagent'], {'runtime.go', 'process.go', 'cleanup_diagnostic.go', 'groupobserver.go', 'start_diagnostic.go', 'terminal_disposition.go', mac[target]} | ({'groupobserver_darwin.go'} if target == 'darwin' else set()))
                self.assertEqual(inventory[prefix + 'cmd/rescue-mac-agent'], {'main.go', 'config.go', 'status.go', 'status_darwin.go' if target == 'darwin' else 'status_other.go', 'status_clock_darwin.go' if target == 'darwin' else 'status_clock_other.go'})
                self.assertEqual(inventory[prefix + 'internal/maclaunch'], {'approval.go', 'terminal_darwin.go' if target == 'darwin' else 'terminal_other.go'})
                self.assertEqual(inventory[prefix + 'internal/agentkeys'], {'loader.go', 'path.go', 'policy.go', 'acl.go', 'source.go', 'format.go', 'trust_config.go', {'linux': 'native_linux.go', 'darwin': 'native_darwin.go', 'windows': 'native_windows.go'}[target]})
                for suffix in list(common) + ['cmd/rescue-mac-agent', 'internal/macagent', 'internal/maclaunch', 'internal/agentkeys']:
                    for fault in ('missing-package', 'unknown-file'):
                        actual = go_ci.product_files_for(target)
                        if fault == 'missing-package':
                            del actual[prefix + suffix]
                        else:
                            actual[prefix + suffix].add('fake_transport.go')
                        self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                other = 'native_darwin.go' if mac[target] == 'native_other.go' else 'native_other.go'
                actual[prefix + 'internal/macagent'].add(other)
                self.assert_build_boundary_rejected(target, actual)

    def test_workload_reclaim_source_is_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn('workload_reclaim.go', go_ci.product_files_for(target)[package])
                actual = go_ci.product_files_for(target)
                actual[package] = (actual[package] | {'workload_reclaim.go'}) - {'workload_reclaim.go'}
                self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('workload_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_auth_exchange_ownership_sources_are_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        required = {'auth_exchange.go', 'auth_exchange_endpoint.go', 'auth_exchange_worker.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertTrue(required <= go_ci.product_files_for(target)[package])
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('auth_exchange_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_service_auth_adapter_source_is_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn('auth_dispatch.go', go_ci.product_files_for(target)[package])
                actual = go_ci.product_files_for(target)
                actual[package] = (actual[package] | {'auth_dispatch.go'}) - {'auth_dispatch.go'}
                self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('auth_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_auth_helper_command_is_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-auth-helper'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory.get(package), {'main.go'})
                for fault in ('missing-package', 'missing-main', 'unknown-file'):
                    actual = go_ci.product_files_for(target)
                    if fault == 'missing-package':
                        del actual[package]
                    elif fault == 'missing-main':
                        actual[package].remove('main.go')
                    else:
                        actual[package].add('unreviewed.go')
                    self.assert_build_boundary_rejected(target, actual)

    def assert_build_boundary_rejected(self, target, actual):
        metadata = '\n'.join(json.dumps({'ImportPath': package, 'GoFiles': sorted(files)})
                             for package, files in actual.items())
        results = [subprocess.CompletedProcess([], 0, stdout=target),
                   subprocess.CompletedProcess([], 0, stdout=metadata)]
        with patch.object(go_ci, 'command', side_effect=results) as calls, \
                contextlib.redirect_stdout(io.StringIO()), \
                self.assertRaisesRegex(ValueError, 'Production file/package boundary changed'):
            go_ci.build()
        self.assertEqual(calls.call_count, 2, 'Invalid inventory reached vet/build')

    def test_guest_sources_are_explicit_for_each_target(self):
        common = {'close.go', 'lifecycle.go', 'model.go', 'plan.go', 'prepare.go',
                  'recovery.go', 'source.go', 'validation.go', 'account_query.go',
                  'account_query_buffer.go', 'account_query_read.go', 'journal.go',
                  'journal_codec.go', 'journal_security.go', 'sid_codec.go',
                  'sid_run.go', 'sid_run_io.go', 'sid_script.go', 'sid_source.go',
                  'creation_codec.go', 'creation_native.go', 'creation_query.go',
                  'creation_script.go', 'creation_source.go', 'sid_script_parts.go',
                  'permission_aliases.go', 'permission_aliases_buffer.go',
                  'permission_aliases_native.go', 'permission_aliases_read.go',
                  'permission_creation.go', 'permission_facts.go', 'permission_facts_buffer.go',
                  'permission_groups.go', 'permission_inspect.go', 'permission_objects.go',
                  'permission_objects_buffer.go', 'permission_policy.go',
                  'staging.go', 'staging_borrow.go', 'staging_native.go',
                  'staging_profile.go', 'staging_query.go', 'never_created.go', 'never_created_session.go',
                  'fixed_layout.go', 'fixed_layout_strict.go', 'fixed_layout_validate.go', 'fixed_layout_wire.go',
                  'runtime_close.go', 'runtime_credential_io.go', 'runtime_credential_run.go',
                  'runtime_credential_script.go', 'runtime_expectation.go', 'runtime_guest.go',
                  'runtime_layout.go', 'runtime_native.go', 'runtime_ops.go', 'runtime_probe.go',
                  'runtime_rights.go', 'runtime_session.go'}
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount'
        child = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                selected = ({'account_query_windows.go', 'account_query_windows_machine.go',
                             'journal_windows.go', 'journal_windows_pin.go', 'creation_query_windows.go',
                             'permission_aliases_windows.go', 'permission_facts_windows.go',
                             'permission_native_windows.go', 'permission_objects_windows.go',
                             'permission_objects_windows_identity.go', 'staging_native_windows.go', 'runtime_native_windows.go', 'runtime_rights_windows.go', 'runtime_token_windows.go'} if target == 'windows'
                            else {'account_query_other.go', 'journal_other.go', 'creation_query_other.go',
                                  'permission_aliases_other.go', 'permission_facts_other.go',
                                  'permission_native_other.go', 'permission_objects_other.go', 'staging_native_other.go', 'runtime_native_other.go'})
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], common | selected)
                self.assertIn('fixed_command.go', inventory[child])
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), {'linux': 321, 'darwin': 322, 'windows': 352}[target])
                for missing in common | selected:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                for unknown in ('foreign.go', 'sid_source_test.go',
                                'account_query_other.go' if target == 'windows' else 'account_query_windows.go',
                                'creation_query_other.go' if target == 'windows' else 'creation_query_windows.go',
                                'permission_aliases_other.go' if target == 'windows' else 'permission_aliases_windows.go',
                                'permission_facts_other.go' if target == 'windows' else 'permission_facts_windows.go',
                                'permission_native_other.go' if target == 'windows' else 'permission_native_windows.go',
                                'permission_objects_other.go' if target == 'windows' else 'permission_objects_windows.go',
                                'permission_inspect_test.go', 'staging_test.go',
                                'staging_native_other.go' if target == 'windows' else 'staging_native_windows.go'):
                    actual = go_ci.product_files_for(target)
                    actual[package].add(unknown)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[child].remove('fixed_command.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_operator_policy_suppliers_are_explicit_and_required(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        for target in ('linux', 'darwin', 'windows'):
            for suffix, name in (('servicehost', 'operator_sid.go'),
                                 ('internal/serviceprovision', 'operator_policy.go')):
                package = prefix + suffix
                with self.subTest(target=target, package=package):
                    self.assertIn(name, go_ci.product_files_for(target)[package])
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(name)
                    self.assert_build_boundary_rejected(target, actual)
                    actual = go_ci.product_files_for(target)
                    actual[package].add(name.removesuffix('.go') + '_test.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_operator_sid_sources_are_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth'
        for target in ('linux', 'darwin', 'windows'):
            for name in ('authenticated_report.go', 'operator_binding.go', 'sid_policy.go', 'sid_profile_frame.go'):
                with self.subTest(target=target, missing=name):
                    self.assertIn(name, go_ci.product_files_for(target)[package])
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(name)
                    self.assert_build_boundary_rejected(target, actual)
            actual = go_ci.product_files_for(target)
            actual[package].add('operator_binding_test.go')
            self.assert_build_boundary_rejected(target, actual)

    def test_staging_owner_sources_are_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                expected = go_ci.product_files_for(target)
                for name in ('guest_staging.go', 'guest_staging_dispatch.go'):
                    self.assertIn(name, expected[package])
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(name)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('guest_staging_test.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_never_created_producer_sources_are_explicit_and_required(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/'
        common = {
            'guestaccount': {'never_created.go', 'never_created_session.go'},
            'serviceprovision': {'staging_execution.go', 'staging_preview.go', 'operator_policy.go'},
        }
        for target in ('linux', 'darwin', 'windows'):
            for suffix, names in common.items():
                package = prefix + suffix
                for name in names:
                    with self.subTest(target=target, package=package, missing=name):
                        self.assertIn(name, go_ci.product_files_for(target)[package])
                        actual = go_ci.product_files_for(target)
                        actual[package].remove(name)
                        self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('never_created_producer_test.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_staging_split_source_is_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn('endpoint_staging.go', go_ci.product_files_for(target)[package])
                actual = go_ci.product_files_for(target)
                actual[package].remove('endpoint_staging.go')
                self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('endpoint_staging_test.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_fixed_guest_sources_are_required_and_os_bounded(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/'
        common = {
            'guestaccount': {'fixed_layout.go', 'fixed_layout_strict.go',
                             'fixed_layout_validate.go', 'fixed_layout_wire.go'},
            'serviceprovision': {'guest_source.go'},
            'serviceready': {'inspection_lease.go', 'inspection_lease_security.go',
                             'inspection_lease_token.go'},
        }
        windows = {'inspection_lease_identity_windows.go', 'inspection_lease_windows.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                for owner, files in common.items():
                    required = files | (windows if owner == 'serviceready' and target == 'windows' else set())
                    self.assertTrue(required <= inventory[prefix + owner])
                    for name in required:
                        actual = go_ci.product_files_for(target)
                        actual[prefix + owner].remove(name)
                        self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[prefix + 'serviceready'].add('inspection_lease_test.go')
                self.assert_build_boundary_rejected(target, actual)
                if target != 'windows':
                    for name in windows:
                        self.assertNotIn(name, inventory[prefix + 'serviceready'])
                        actual = go_ci.product_files_for(target)
                        actual[prefix + 'serviceready'].add(name)
                        self.assert_build_boundary_rejected(target, actual)

    def test_confirmation_sources_are_limited_to_the_two_owners(self):
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                for owner in ('servicehost', 'internal/serviceprovision'):
                    package = 'github.com/O2-CONNECTIVE/rescue-host/backend/' + owner
                    expected = go_ci.product_files_for(target)
                    self.assertIn('confirmation.go', expected[package])
                    actual = go_ci.product_files_for(target)
                    actual[package].remove('confirmation.go')
                    self.assert_build_boundary_rejected(target, actual)
                    actual = go_ci.product_files_for(target)
                    actual[package].add('confirmation_test.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_confirmed_grant_sources_are_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                expected = go_ci.product_files_for(target)
                for name in ('confirmed_grant.go', 'confirmed_grant_snapshot.go'):
                    self.assertIn(name, expected[package])
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(name)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('confirmed_grant_test.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_preview_sources_are_explicit_and_bounded(self):
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                service = inventory['github.com/O2-CONNECTIVE/rescue-host/backend/servicehost']
                provision = inventory['github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceprovision']
                self.assertTrue({'preview.go', 'preview_dispatch.go'} <= service)
                self.assertIn('preview.go', provision)
                self.assertNotIn('preview_test.go', service)
                for package, name in (
                        ('servicehost', 'preview_dispatch.go'),
                        ('internal/serviceprovision', 'preview.go')):
                    actual = go_ci.product_files_for(target)
                    key = 'github.com/O2-CONNECTIVE/rescue-host/backend/' + package
                    actual[key].remove(name)
                    self.assert_build_boundary_rejected(target, actual)

    def test_service_control_sources_follow_selected_go_target(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol'
        for target, production_count in (('linux', 321), ('darwin', 322), ('windows', 352)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertIn(package, inventory)
                self.assertEqual(inventory[package], {'request.go', 'strict.go', 'response.go', 'response_fields.go', 'response_preview.go'})
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_build_rejects_invalid_service_control_inventory(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol'
        for target in ('linux', 'darwin', 'windows'):
            for fault in ('missing-package', 'missing-file', 'unknown-file'):
                with self.subTest(target=target, fault=fault):
                    actual = go_ci.product_files_for(target)
                    actual[package] = {'request.go', 'strict.go', 'response.go', 'response_fields.go', 'response_preview.go'}
                    if fault == 'missing-package':
                        del actual[package]
                    elif fault == 'missing-file':
                        actual[package].remove('strict.go')
                    else:
                        actual[package].add('future.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_build_rejects_each_missing_response_source(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol'
        for target in ('linux', 'darwin', 'windows'):
            for missing in ('response.go', 'response_fields.go', 'response_preview.go'):
                with self.subTest(target=target, missing=missing):
                    actual = go_ci.product_files_for(target)
                    actual[package] = {'request.go', 'strict.go', 'response.go',
                                       'response_fields.go', 'response_preview.go'}
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)

    def test_dispatch_owner_sources_are_explicit_and_complete(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertTrue({'dispatch.go', 'dispatch_owner.go'} <= inventory[package])
                for missing in ('dispatch.go', 'dispatch_owner.go'):
                    actual = go_ci.product_files_for(target)
                    actual[package] = (actual[package] | {'dispatch.go', 'dispatch_owner.go'}) - {missing}
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('dispatch_native_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_owner_reservation_source_is_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertIn('reservation.go', inventory[package])
                actual = go_ci.product_files_for(target)
                actual[package] = (actual[package] | {'reservation.go'}) - {'reservation.go'}
                self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('reservation_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_host_delivery_source_is_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertIn('delivery.go', inventory[package])
                actual = go_ci.product_files_for(target)
                actual[package] = (actual[package] | {'delivery.go'}) - {'delivery.go'}
                self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('delivery_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_endpoint_candidate_sources_are_required_and_target_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                selected = ({'endpoint_candidate_windows.go', 'endpoint_profile_windows.go'}
                            if target == 'windows' else {'endpoint_profile_other.go'})
                required = {'endpoint_candidate.go', 'sole_input_batch.go', 'authenticated_report.go', 'operator_binding.go', 'sid_policy.go', 'sid_profile_frame.go'} | selected
                self.assertTrue(required <= go_ci.product_files_for(target)[package])
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('endpoint_profile_other.go' if target == 'windows' else 'endpoint_profile_windows.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_native_profile_scope_sources_are_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth'
        common = {'batch_scope.go', 'profile_provider.go', 'profile_query.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                selected = 'profile_query_windows.go' if target == 'windows' else 'profile_query_other.go'
                self.assertTrue(common | {selected} <= go_ci.product_files_for(target)[package])
                for missing in common | {selected}:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('profile_query_other.go' if target == 'windows' else 'profile_query_windows.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_operator_auth_sources_follow_selected_target(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth'
        common = {'collector.go', 'controller.go', 'helper.go', 'types.go', 'launch.go', 'native_batch.go', 'report.go', 'report_cleanup.go', 'report_endpoint.go', 'report_operation.go', 'report_owner.go', 'report_pair.go', 'profile_frame.go', 'profile_stream.go', 'profile_sync.go', 'profile_pipe.go', 'profile_receiver.go', 'helper_entry.go', 'helper_entry_owner.go', 'prepared_batch.go', 'batch_scope.go', 'profile_provider.go', 'profile_query.go', 'endpoint_candidate.go', 'sole_input_batch.go', 'authenticated_report.go', 'operator_binding.go', 'sid_policy.go', 'sid_profile_frame.go', 'supplier.go', 'supplier_config.go', 'helper_launch_source.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                selected = ({'native_windows.go', 'native_token_windows.go', 'native_process_windows.go', 'native_batch_windows.go', 'native_launch_binding_windows.go', 'native_launch_image_windows.go', 'native_launch_windows.go', 'native_report_operation_windows.go', 'native_report_principal_windows.go', 'native_report_windows.go', 'profile_sync_windows.go', 'profile_pipe_windows.go', 'profile_receiver_windows.go', 'helper_entry_windows.go', 'profile_query_windows.go', 'endpoint_candidate_windows.go', 'endpoint_profile_windows.go', 'supplier_image_windows.go', 'supplier_native_windows.go', 'supplier_security_windows.go', 'helper_launch_native_windows.go'}
                            if target == 'windows' else {'native_other.go', 'native_batch_other.go', 'native_launch_other.go', 'native_report_other.go', 'profile_sync_other.go', 'profile_pipe_other.go', 'profile_receiver_other.go', 'helper_entry_other.go', 'profile_query_other.go', 'endpoint_profile_other.go', 'helper_launch_native_other.go'})
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], common | selected)
                self.assertEqual(len(inventory), 42)

    def test_operator_auth_missing_or_foreign_files_stop_before_vet(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth'
        common = {'collector.go', 'controller.go', 'helper.go', 'types.go', 'launch.go', 'native_batch.go', 'report.go', 'report_cleanup.go', 'report_endpoint.go', 'report_operation.go', 'report_owner.go', 'report_pair.go', 'profile_frame.go', 'profile_stream.go', 'profile_sync.go', 'profile_pipe.go', 'profile_receiver.go', 'helper_entry.go', 'helper_entry_owner.go', 'prepared_batch.go', 'batch_scope.go', 'profile_provider.go', 'profile_query.go', 'endpoint_candidate.go', 'sole_input_batch.go', 'authenticated_report.go', 'operator_binding.go', 'sid_policy.go', 'sid_profile_frame.go', 'supplier.go', 'supplier_config.go', 'helper_launch_source.go'}
        for target in ('linux', 'darwin', 'windows'):
            selected = ({'native_windows.go', 'native_token_windows.go', 'native_process_windows.go', 'native_batch_windows.go', 'native_launch_binding_windows.go', 'native_launch_image_windows.go', 'native_launch_windows.go', 'native_report_operation_windows.go', 'native_report_principal_windows.go', 'native_report_windows.go', 'profile_sync_windows.go', 'profile_pipe_windows.go', 'profile_receiver_windows.go', 'helper_entry_windows.go', 'profile_query_windows.go', 'endpoint_candidate_windows.go', 'endpoint_profile_windows.go', 'supplier_image_windows.go', 'supplier_native_windows.go', 'supplier_security_windows.go', 'helper_launch_native_windows.go'}
                        if target == 'windows' else {'native_other.go', 'native_batch_other.go', 'native_launch_other.go', 'native_report_other.go', 'profile_sync_other.go', 'profile_pipe_other.go', 'profile_receiver_other.go', 'helper_entry_other.go', 'profile_query_other.go', 'endpoint_profile_other.go', 'helper_launch_native_other.go'})
            for fault in ('missing-package', *sorted(common | selected), 'foreign-os', 'unknown-file'):
                with self.subTest(target=target, fault=fault):
                    actual = go_ci.product_files_for(target)
                    actual[package] = common | selected
                    if fault == 'missing-package':
                        del actual[package]
                    elif fault == 'foreign-os':
                        actual[package].add('native_other.go' if target == 'windows' else 'native_windows.go')
                    elif fault == 'unknown-file':
                        actual[package].add('unreviewed.go')
                    else:
                        actual[package].remove(fault)
                    self.assert_build_boundary_rejected(target, actual)

    def provision_sources(self, target):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        common = {'compose.go', 'errors.go', 'record.go', 'record_decode.go',
                  'record_strict.go', 'record_validate.go', 'root.go', 'root_doc.go',
                  'root_io.go', 'root_validate.go', 'service.go', 'preview.go', 'confirmation.go', 'execution.go',
                  'execution_native.go', 'execution_owner.go', 'launch.go', 'launch_security.go', 'guest_source.go',
                  'staging_execution.go', 'staging_preview.go', 'operator_policy.go', 'runtime_execution.go', 'runtime_native.go', 'runtime_source.go'}
        selected = ({'root_windows.go', 'root_windows_io.go', 'launch_windows.go',
                     'launch_windows_security.go', 'launch_windows_threads.go'} if target == 'windows'
                    else {'root_other.go', 'launch_other.go'})
        return {prefix + 'internal/serviceprovision': common | selected,
                prefix + 'cmd/rescue-service-host': {'main.go'}}

    def test_service_provision_sources_follow_selected_go_target(self):
        for target, production_count in (('linux', 321), ('darwin', 322), ('windows', 352)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                for package, files in self.provision_sources(target).items():
                    self.assertIn(package, inventory)
                    self.assertEqual(inventory[package], files)
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_build_rejects_invalid_service_provision_inventory(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        provision, command = prefix + 'internal/serviceprovision', prefix + 'cmd/rescue-service-host'
        for target in ('linux', 'darwin', 'windows'):
            for fault in ('missing-provision', 'missing-command', 'missing-common',
                          'wrong-root-os', 'foreign-os-file', 'unknown-provision-file', 'unknown-command-file'):
                with self.subTest(target=target, fault=fault):
                    actual = go_ci.product_files_for(target)
                    actual.update(self.provision_sources(target))
                    if fault == 'missing-provision':
                        del actual[provision]
                    elif fault == 'missing-command':
                        del actual[command]
                    elif fault == 'missing-common':
                        actual[provision].remove('root_validate.go')
                    elif fault == 'wrong-root-os':
                        selected = 'root_windows.go' if target == 'windows' else 'root_other.go'
                        actual[provision].remove(selected)
                        actual[provision].add('root_other.go' if target == 'windows' else 'root_windows.go')
                    elif fault == 'foreign-os-file':
                        actual[provision].add('root_other.go' if target == 'windows' else 'root_windows_io.go')
                    else:
                        actual[command if fault == 'unknown-command-file' else provision].add('future.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_scm_sources_follow_selected_go_target(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target, selected, excluded, production_count in (
                ('linux', 'scm_other.go', 'scm_windows.go', 321),
                ('darwin', 'scm_other.go', 'scm_windows.go', 322),
                ('windows', 'scm_windows.go', 'scm_other.go', 352)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], {'control.go', 'host.go', 'approved.go',
                                                      'job.go', 'ownercontrol.go', 'issued.go', 'nativebootstrap.go', 'request_loop.go',
                                                      'nativestdio.go', 'ownedstdio.go', 'scm_ready.go', 'service.go', 'outer.go',
                                          'dispatch.go', 'dispatch_owner.go', 'reservation.go', 'delivery.go', 'auth_dispatch.go', 'workload_reclaim.go', 'auth_exchange.go', 'auth_exchange_endpoint.go', 'auth_exchange_worker.go', 'control_factory.go', 'control_readiness.go', 'control_exchange.go', 'preview.go', 'preview_dispatch.go', 'confirmation.go', 'confirmed_grant.go', 'confirmed_grant_snapshot.go', 'confirmed_target.go', 'guest_staging.go', 'guest_staging_dispatch.go', 'operator_sid.go', 'runtime_guest.go', 'runtime_guest_actor.go', 'runtime_guest_native.go', 'runtime_guest_prepare.go', 'runtime_guest_admission.go', 'runtime_guest_evaluation.go',
                                                      'scm_ready_windows.go' if target == 'windows' else 'scm_ready_other.go',
                                                      'nativebootstrap_windows.go' if target == 'windows' else 'nativebootstrap_other.go', selected})
                self.assertNotIn(excluded, inventory[package])
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_localcontrol_sources_follow_selected_go_target(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/localcontrol'
        for target, selected, excluded, production_count in (
                ('linux', {'native_other.go'}, 'native_windows.go', 321),
                ('darwin', {'native_other.go'}, 'native_windows.go', 322),
                ('windows', {'native_windows.go', 'io_windows.go'}, 'native_other.go', 352)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], {'policy.go', 'boundary.go', 'wire.go'} | selected)
                self.assertNotIn(excluded, inventory[package])
                if target != 'windows':
                    self.assertNotIn('io_windows.go', inventory[package])
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_runtime_and_childjob_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/'
        for target, selected, excluded, production_count in (
                ('linux', {'native_other.go'}, {'native_windows.go', 'artifact_windows.go', 'prepared_windows.go', 'guest_token_windows.go', 'protected_guest_windows.go'}, 321),
                ('darwin', {'native_other.go'}, {'native_windows.go', 'artifact_windows.go', 'prepared_windows.go', 'guest_token_windows.go', 'protected_guest_windows.go'}, 322),
                ('windows', {'native_windows.go', 'artifact_windows.go', 'prepared_windows.go', 'guest_token_windows.go', 'protected_guest_windows.go'}, {'native_other.go'}, 352)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'runbackend'], {'runtime.go'})
                self.assertEqual(inventory[prefix + 'childjob'], {'child.go', 'plan.go', 'fixed_command.go', 'prepared.go', 'inspection.go', 'guest_token.go', 'guest_token_policy.go', 'guest_token_buffer.go', 'protected_guest.go'} | selected)
                self.assertFalse(inventory[prefix + 'childjob'] & excluded)
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_protectedkey_and_bootstrap_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/'
        common = {'acl.go', 'format.go', 'loader.go', 'profile.go', 'scope.go',
                  'source.go', 'validation.go'}
        for target, selected, excluded, production_count in (
                ('linux', 'native_other.go', 'native_windows.go', 321),
                ('darwin', 'native_other.go', 'native_windows.go', 322),
                ('windows', 'native_windows.go', 'native_other.go', 352)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'protectedkey'], common | {selected})
                self.assertNotIn(excluded, inventory[prefix + 'protectedkey'])
                self.assertEqual(inventory[prefix + 'bootstrap'],
                                 {'codec.go', 'launch.go', 'native_receiver.go', 'strict.go', 'types.go', 'validate.go',
                                  'native_receiver_windows.go' if target == 'windows' else 'native_receiver_other.go'})
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_native_bootstrap_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        auth_common = {'auth.go', 'image.go', 'kernel.go', 'profile.go', 'validation.go', 'prepared_service.go', 'prepared_lease.go'}
        for target, auth_native, io_native, production_count in (
                ('linux', {'native_other.go'}, 'native_other.go', 321),
                ('darwin', {'native_other.go'}, 'native_other.go', 322),
                ('windows', {'native_windows.go', 'image_windows.go'}, 'native_windows.go', 352)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'internal/bootstrapauth'], auth_common | auth_native)
                self.assertEqual(inventory[prefix + 'internal/bootstrapio'], {'io.go', io_native})
                self.assertEqual(inventory[prefix + 'cmd/rescue-backend'], {'main.go', 'native.go'})
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), production_count)
                if target != 'windows':
                    self.assertNotIn('image_windows.go', inventory[prefix + 'internal/bootstrapauth'])

    def test_owned_stdio_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        for target, selected, excluded in (
                ('linux', 'native_other.go', 'native_windows.go'),
                ('darwin', 'native_other.go', 'native_windows.go'),
                ('windows', 'native_windows.go', 'native_other.go')):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'internal/childstdio'],
                                 {'operation.go', 'pipes.go', 'streams.go', selected})
                self.assertNotIn(excluded, inventory[prefix + 'internal/childstdio'])
                self.assertTrue({'nativestdio.go', 'ownedstdio.go'} <= inventory[prefix + 'servicehost'])
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), {'linux': 321, 'darwin': 322, 'windows': 352}[target])

    def test_service_query_witness_is_required_and_unknown_readiness_sources_rejected(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn('service_witness.go', go_ci.product_files_for(target)[package])
                for fault in ('missing-witness', 'unknown-source'):
                    actual = go_ci.product_files_for(target)
                    if fault == 'missing-witness':
                        actual[package].remove('service_witness.go')
                    else:
                        actual[package].add('unreviewed_readiness.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_exchange_endpoint_sources_are_required_and_target_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready'
        common = {'endpoint.go', 'endpoint_input.go', 'endpoint_operation.go', 'endpoint_staging.go'}
        windows = {'endpoint_windows.go', 'endpoint_io_windows.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                required = common | (windows if target == 'windows' else set())
                self.assertTrue(required <= go_ci.product_files_for(target)[package])
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('endpoint_unreviewed.go' if target == 'windows' else 'endpoint_windows.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_zero_grant_ready_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        common = {'config.go', 'operation.go', 'plane.go', 'security.go', 'process_token.go', 'preflight.go', 'service_witness.go', 'endpoint.go', 'endpoint_input.go', 'endpoint_operation.go', 'endpoint_staging.go',
                  'inspection_lease.go', 'inspection_lease_security.go', 'inspection_lease_token.go'}
        for target, selected, excluded in (
                ('linux', {'native_other.go'}, {'native_windows.go', 'process_token_windows.go', 'connect_windows.go', 'listener_windows.go', 'endpoint_windows.go', 'endpoint_io_windows.go', 'inspection_lease_identity_windows.go', 'inspection_lease_windows.go'}),
                ('darwin', {'native_other.go'}, {'native_windows.go', 'process_token_windows.go', 'connect_windows.go', 'listener_windows.go', 'endpoint_windows.go', 'endpoint_io_windows.go', 'inspection_lease_identity_windows.go', 'inspection_lease_windows.go'}),
                ('windows', {'native_windows.go', 'process_token_windows.go', 'connect_windows.go', 'listener_windows.go', 'endpoint_windows.go', 'endpoint_io_windows.go', 'inspection_lease_identity_windows.go', 'inspection_lease_windows.go'}, {'native_other.go'})):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'internal/serviceready'], common | selected)
                self.assertFalse(inventory[prefix + 'internal/serviceready'] & excluded)
                self.assertIn('prepared_service.go', inventory[prefix + 'internal/bootstrapauth'])
                self.assertTrue({'scm_ready.go', 'service.go'} <= inventory[prefix + 'servicehost'])
                self.assertIn('scm_ready_windows.go' if target == 'windows' else 'scm_ready_other.go',
                              inventory[prefix + 'servicehost'])
                self.assertEqual(len(inventory), 42)
                self.assertEqual(sum(map(len, inventory.values())), {'linux': 321, 'darwin': 322, 'windows': 352}[target])

    def launch_owner_additions(self, target):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        selected = ({'launch_windows.go', 'launch_windows_security.go', 'launch_windows_threads.go'}
                    if target == 'windows' else {'launch_other.go'})
        return {
            prefix + 'internal/serviceprovision': {
                'execution.go', 'execution_native.go', 'execution_owner.go',
                'launch.go', 'launch_security.go'} | selected,
            prefix + 'internal/serviceready': {'preflight.go'},
            prefix + 'servicehost': {'outer.go'},
        }

    def test_each_launch_owner_source_is_required_before_build(self):
        for target in ('linux', 'darwin', 'windows'):
            for package, files in self.launch_owner_additions(target).items():
                for missing in files:
                    with self.subTest(target=target, package=package, missing=missing):
                        actual = go_ci.product_files_for(target)
                        for key, additions in self.launch_owner_additions(target).items():
                            actual[key].update(additions)
                        actual[package].remove(missing)
                        self.assert_build_boundary_rejected(target, actual)

    def test_launch_owner_foreign_os_source_is_rejected_before_build(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceprovision'
        for target in ('linux', 'darwin', 'windows'):
            foreign = ({'launch_other.go'} if target == 'windows' else
                       {'launch_windows.go', 'launch_windows_security.go', 'launch_windows_threads.go'})
            for added in foreign:
                with self.subTest(target=target, added=added):
                    actual = go_ci.product_files_for(target)
                    for key, additions in self.launch_owner_additions(target).items():
                        actual[key].update(additions)
                    actual[package].add(added)
                    self.assert_build_boundary_rejected(target, actual)

    def test_guest_sources_are_exact_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount'
        required = {'model.go', 'prepare.go', 'plan.go', 'close.go', 'recovery.go', 'source.go', 'validation.go', 'lifecycle.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertTrue(required <= inventory.get(package, set()))
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('unreviewed_source.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_control_composition_sources_are_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        required = {'control_factory.go', 'control_readiness.go', 'control_exchange.go', 'request_loop.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertTrue(required <= go_ci.product_files_for(target)[package])
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('unreviewed_control_provider.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_sole_sources_are_exact_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth'
        required = {'sole_input_batch.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertTrue(required <= inventory.get(package, set()))
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('unreviewed_source.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_inspection_bridge_sources_are_exact_and_required(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        for target in ('linux', 'darwin', 'windows'):
            for suffix, source in (('internal/childjob', 'inspection.go'),
                                   ('servicehost', 'confirmed_target.go')):
                with self.subTest(target=target, source=source):
                    package = prefix + suffix
                    self.assertIn(source, go_ci.product_files_for(target)[package])
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(source)
                    self.assert_build_boundary_rejected(target, actual)
                    actual = go_ci.product_files_for(target)
                    actual[package].add('unreviewed_activation.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_guest_token_read_sources_are_exact_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob'
        common = {'guest_token.go', 'guest_token_policy.go', 'guest_token_buffer.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                required = common | ({'guest_token_windows.go'} if target == 'windows' else set())
                self.assertTrue(required <= go_ci.product_files_for(target)[package])
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('unreviewed_guest_logon.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_unknown_go_target_is_rejected(self):
        for target in ('', 'plan9', 'windows/../linux'):
            with self.subTest(target=target), self.assertRaises(ValueError):
                go_ci.product_files_for(target)


if __name__ == '__main__':
    unittest.main()

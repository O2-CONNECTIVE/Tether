"""Single post-verdict diagnostic: exact source qualification and one invocation."""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
import re
import secrets
from pathlib import Path
import subprocess
import sys
import unittest

SOURCE = 'cad02a5b6fefd3ac11a4b8b45758feb438bf024b'
TREE = '3bb45eb8c459761898bfff160a4931ed895e4463'
SECTIONS = {'steps', 'native', 'observation', 'wry', 'delivery', 'receiver',
            'driver', 'host', 'launch', 'final', 'overlay', 'build'}
# Fixed test IDs are populated only from the reviewed final source, never a count inherited from an older variant.
TEST_IDS = ['test_post_verdict_failure.FailureAdmissionTests.test_bad_timing_rejected',
 'test_post_verdict_failure.FailureAdmissionTests.test_bounded_regular_unique_file_boundary',
 'test_post_verdict_failure.FailureAdmissionTests.test_duplicate_keys_rejected',
 'test_post_verdict_failure.FailureAdmissionTests.test_headers_success_extra_and_mismatched_request_rejected',
 'test_post_verdict_failure.FailureAdmissionTests.test_non_target_and_typed_negatives',
 'test_post_verdict_failure.FailureAdmissionTests.test_original_pre_header_timeout_is_admitted',
 'test_post_verdict_failure.FailureAdmissionTests.test_real_copy_receipt_binds_exact_bytes',
 'test_post_verdict_signal.SignalTests.test_actual_collector_accepts_signal_and_natural_finish_wins',
 'test_post_verdict_signal.SignalTests.test_actual_reader_accepts_bound_signal_once',
 'test_post_verdict_signal.SignalTests.test_duplicate_oversized_links_and_stale_manifest_rejected',
 'test_post_verdict_signal.SignalTests.test_partial_candidate_is_never_retried_after_replacement',
 'test_post_verdict_signal.SignalTests.test_rejects_wrong_identity_closed_types_and_noncanonical_bytes',
 'test_wry_delivery.DeliveryTests.test_actual_api_get_dispatch_and_outside_binding',
 'test_wry_delivery.DeliveryTests.test_actual_callback_send_and_pump_error_values_are_preserved',
 'test_wry_delivery.DeliveryTests.test_actual_duplicate_and_missing_tail_mutations_are_detected',
 'test_wry_delivery.DeliveryTests.test_actual_file_boundary_rejects_missing_close_duplicate_truncation_and_alias',
 'test_wry_delivery.DeliveryTests.test_actual_identity_cli_rejects_missing_wrong_pins_and_keeps_previous_record',
 'test_wry_delivery.DeliveryTests.test_actual_materialized_cli_lf_crlf_and_identity_boundaries',
 'test_wry_delivery.DeliveryTests.test_actual_nested_wrong_token_other_thread_and_unwrapped_wait_are_unknown',
 'test_wry_delivery.DeliveryTests.test_actual_publisher_preserves_native_verdict_and_partial_phase_evidence',
 'test_wry_delivery.DeliveryTests.test_both_inverse_projections_restore_exact_upstream_bytes',
 'test_wry_delivery.DeliveryTests.test_closed_shapes_and_unknown_partial_never_authorize',
 'test_wry_delivery.DeliveryTests.test_exact_lock_lf_crlf_and_wrong_or_already_projected_inputs',
 'test_wry_delivery.DeliveryTests.test_preparer_wrong_archive_existing_output_and_non_secret_cli',
 'test_wry_delivery_fetch.FetchTests.test_actual_omitted_sibling_then_published_exact_source',
 'test_wry_delivery_fetch.FetchTests.test_actual_workflow_pins_and_missing_changed_duplicate_pins',
 'test_wry_delivery_fetch.FetchTests.test_rejected_cli_never_copies_subprocess_output',
 'test_wry_delivery_fetch.FetchTests.test_wrong_pin_shape_never_runs_git',
 'test_wry_post_verdict.AnchorTests.test_actual_send_before_anchor_or_zero_capture_never_qualifies',
 'test_wry_post_verdict.AnchorTests.test_closed_reader_and_invalid_order_loss',
 'test_wry_post_verdict.AnchorTests.test_companion_file_boundary_and_exact_projection',
 'test_wry_post_verdict.AnchorTests.test_original_send_after_verdict_and_entry_before_verdict',
 'test_wry_post_verdict.AnchorTests.test_real_recorder_deterministic_races',
 'test_wry_probe.OverlayTests.test_actual_cli_invalid_arguments_never_echo_values_or_paths',
 'test_wry_probe.OverlayTests.test_actual_cli_text_stream_accepts_only_exact_lf_or_crlf',
 'test_wry_probe.OverlayTests.test_actual_lock_projection_accepts_only_two_exact_representations',
 'test_wry_probe.OverlayTests.test_pinned_archive_actual_projection',
 'test_wry_probe.OverlayTests.test_real_cross_crate_hook_and_rust_record_reach_strict_reader',
 'test_wry_probe.RecordTests.test_actual_file_boundary_rejects_unknown_closed_symlink_and_noncanonical',
 'test_wry_probe.RecordTests.test_actual_publisher_requires_all_critical_sections_and_handles_bad_host_rows',
 'test_wry_probe.RecordTests.test_actual_publisher_subprocess_keeps_partial_failure_safe',
 'test_wry_probe.RecordTests.test_atomic_checkpoint_failure_preserves_previous_prefix',
 'test_wry_probe.RecordTests.test_complete_partial_and_callback_before_api',
 'test_wry_probe.RecordTests.test_exact_generated_permission_contract_and_alias_negatives',
 'test_wry_probe.RecordTests.test_public_projection_never_copies_arbitrary_strings_or_keys',
 'test_wry_probe.RecordTests.test_retained_actual_failure_projection_stays_red',
 'test_wry_probe.RecordTests.test_unknown_missing_duplicate_count_loss_time_and_thread_are_rejected',
 'test_wry_receiver.ReceiverTests.test_actual_seam_and_negative_controls']


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode('ascii')


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL).strip().decode()


def binding(root):
    control_root = Path(__file__).resolve().parents[1]
    control = git(control_root, 'rev-parse', 'HEAD')
    git(control_root, 'diff', '--exit-code', 'HEAD')
    if (os.environ.get('CONTROL_HEAD') != control or os.environ.get('WRY_PIN') != SOURCE
            or os.environ.get('WRY_TREE') != TREE or git(root, 'rev-parse', 'HEAD') != SOURCE
            or git(root, 'rev-parse', 'HEAD^{tree}') != TREE):
        raise ValueError('PIN_REJECTED')
    return {'controlCommit': control, 'sourceCommit': SOURCE, 'sourceTree': TREE}


def source_module(root):
    sys.path.insert(0, str(root / 'scripts'))
    spec = importlib.util.spec_from_file_location('post_verdict_source', root / 'scripts/wry_probe_ci.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def expected(bind):
    return {'schema': 1, 'qualification': 'post-verdict-source-fixtures', **bind,
            'discovered': len(TEST_IDS), 'started': len(TEST_IDS), 'passed': len(TEST_IDS), 'skipped': 0,
            'testIdsSha256': digest(TEST_IDS),
            'failures': 0, 'errors': 0, 'expectedFailures': 0, 'unexpectedSuccesses': 0,
            'nativeLaunches': 0}


class ExecutedResult(unittest.TextTestResult):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.started = []
        self.passed = []

    def startTest(self, test):
        self.started.append(test.id())
        super().startTest(test)

    def addSuccess(self, test):
        self.passed.append(test.id())
        super().addSuccess(test)


def cases(suite):
    for item in suite:
        if isinstance(item, unittest.TestSuite): yield from cases(item)
        else: yield item


def suite(root):
    loader = unittest.TestLoader()
    return unittest.TestSuite(loader.discover(str(root / 'scripts/tests'), pattern=pattern)
                              for pattern in ('test_wry*.py', 'test_post_verdict_signal.py',
                                              'test_post_verdict_failure.py'))


def qualify(root, bind):
    tests = suite(root)
    ids = sorted(case.id() for case in cases(tests))
    if not ids or ids != sorted(TEST_IDS) or len(set(ids)) != len(ids):
        raise ValueError('QUALIFICATION_REJECTED')
    result = unittest.TextTestRunner(verbosity=2, resultclass=ExecutedResult).run(tests)
    if (sorted(result.started) != ids or sorted(result.passed) != ids or result.testsRun != len(ids)
            or result.skipped or result.failures or result.errors or result.expectedFailures
            or result.unexpectedSuccesses):
        raise ValueError('QUALIFICATION_REJECTED')
    return expected(bind)


def manifest(bind, specimen, run):
    if (not re.fullmatch('[1-9][0-9]{0,19}', run)
            or not re.fullmatch('[0-9a-f]{64}', specimen)):
        raise ValueError('MANIFEST_REJECTED')
    return {'protocol': 'post-verdict-manifest-1', 'run': run, 'attempt': 1, 'trial': 1,
            **bind, 'specimenSha256': specimen, 'nonce': secrets.token_hex(32)}


def write_new(path, value):
    with path.open('xb') as stream: stream.write(canonical(value))


def receipt(temp, source, bind):
    value = source.load(temp / 'post-verdict-qualification.json', 2048)
    if canonical(value) != canonical(expected(bind)): raise ValueError('QUALIFICATION_REJECTED')
    return value


def emit(value):
    print('WRY_POST_VERDICT_QUALIFICATION_BEGIN')
    print(canonical(value).decode())
    print('WRY_POST_VERDICT_QUALIFICATION_SHA256=' + digest(value))
    print('WRY_POST_VERDICT_QUALIFICATION_END', flush=True)


def publish_qualification(temp, source, bind):
    value = receipt(temp, source, bind)
    emit(value)
    write_new(temp / 'post-verdict-qualification-published.json', {'qualificationSha256': digest(value), **bind})


def admit(root, temp, source, bind):
    import post_verdict_engine
    engine_hash = post_verdict_engine.admit(temp, bind)
    value = receipt(temp, source, bind)
    published = source.load(temp / 'post-verdict-qualification-published.json', 2048)
    if canonical(published) != canonical({'qualificationSha256': digest(value), **bind}):
        raise ValueError('QUALIFICATION_PUBLICATION_REJECTED')
    steps = source.safe_steps(source.load(temp / 'steps.json', 4096))
    if canonical(steps) != canonical([{'stage': name, 'exitCode': 0} for name in source.STEPS[:12]]):
        raise ValueError('SETUP_REJECTED')
    source.permissions(root, True)
    identity = source.source_identity(root, temp / 'wry', temp / 'wry.crate', True)
    for relative in ('rescue-host-shell.exe', 'BUILD.json'):
        if (temp / 'unpacked' / relative).read_bytes() != (temp / 'fixture' / relative).read_bytes():
            raise ValueError('SPECIMEN_REJECTED')
    paths = {'exeSha256': 'unpacked/rescue-host-shell.exe', 'driverSha256': 'tools/tauri/bin/tauri-driver.exe',
             'archiveSha256': 'fixture.zip', 'edgeDriverSha256': 'tools/edge/msedgedriver.exe',
             'driverLockSha256': 'tools/tauri-driver-2.1.0/Cargo.lock'}
    identity.update({key: source.digest((temp / path).read_bytes()) for key, path in paths.items()})
    identity['driverPatchSha256'] = source.digest((root / 'tools/native-driver-trace/driver.patch').read_bytes())
    build_manifest = {'schema': 1, 'variant': 'host-startup-diagnostic-1', 'sourceCommit': SOURCE,
                'features': ['custom-protocol', 'startup-diagnostics'], 'profile': 'release',
                'exeSha256': identity['exeSha256'], 'distribution': False}
    if canonical(source.load(temp / 'unpacked/BUILD.json', 2048)) != canonical(build_manifest):
        raise ValueError('SPECIMEN_REJECTED')
    if (identity.get('sourceCommit') != SOURCE or identity.get('sourceTree') != TREE
            or canonical(identity) != canonical(source.safe_meta(source.load(temp / 'launch.json', 4096), delivery=True))):
        raise ValueError('IDENTITY_REJECTED')
    # Disposable run-local duplicate guard, not a persistent exactly-once ledger.
    run = os.environ.get('GITHUB_RUN_ID', '')
    if os.environ.get('GITHUB_RUN_ATTEMPT') != '1': raise ValueError('RUN_REJECTED')
    value_manifest = manifest(bind, identity['exeSha256'], run)
    write_new(temp / 'post-verdict.manifest.json', value_manifest)
    write_new(temp / 'post-verdict-admitted.json', {'qualificationSha256': digest(value),
              'manifestSha256': digest(value_manifest), 'engineQualificationSha256': engine_hash, **bind})


def validate_record(text, bind):
    lines = text.splitlines()
    if (len(text.encode()) > 34000 or len(lines) != 4 or lines[0] != 'WRY_DIAGNOSTIC_RECORD_BEGIN'
            or lines[3] != 'WRY_DIAGNOSTIC_RECORD_END'): raise ValueError('PUBLICATION_REJECTED')
    body = json.loads(lines[1])
    if (canonical(body).decode() != lines[1] or lines[2] != 'WRY_DIAGNOSTIC_RECORD_SHA256=' + digest(body)
            or set(body['files']) != SECTIONS or any(body.get(k) != v for k, v in bind.items())):
        raise ValueError('PUBLICATION_REJECTED')
    for section in body['files'].values():
        if set(section) != {'data', 'sha256'} or section['sha256'] != digest(section['data']):
            raise ValueError('PUBLICATION_REJECTED')
    return body


def publish(root, temp, source, bind):
    output = io.StringIO()
    passed = False
    try:
        with contextlib.redirect_stdout(output): passed = source.publish(root, temp, delivery=True)
    except Exception:
        pass
    text = output.getvalue()
    original = validate_record(text, bind)
    print(text, end='', flush=True)
    from post_verdict_publish import companion
    causal = companion(root, temp, bind, original, digest(original))
    print('WRY_POST_VERDICT_RECORD_BEGIN')
    print(canonical(causal).decode())
    print('WRY_POST_VERDICT_RECORD_SHA256=' + digest(causal))
    print('WRY_POST_VERDICT_RECORD_END', flush=True)
    # Preserve the original verdict. A valid partial/failed record is still printed.
    return 0 if passed is True else 1


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=('qualify', 'qualification', 'engine-qualification', 'admit', 'publish'))
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--temp', type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        bind = binding(args.root)
        if args.mode in ('qualify', 'admit'):
            subprocess.run(['git', '-C', str(args.root), 'diff', '--exit-code', 'HEAD', '--', '.',
                            ':(exclude)ui/tauri/src-tauri/Cargo.lock'], check=True, capture_output=True)
        if args.mode == 'qualify':
            value = qualify(args.root, bind)
            write_new(args.temp / 'post-verdict-qualification.json', value)
        else:
            source = source_module(args.root)
            if args.mode == 'qualification': publish_qualification(args.temp, source, bind)
            elif args.mode == 'engine-qualification':
                import post_verdict_engine
                post_verdict_engine.publish(args.temp, bind)
            elif args.mode == 'admit': admit(args.root, args.temp, source, bind)
            else: return publish(args.root, args.temp, source, bind)
        return 0
    except (OSError, ValueError, TypeError, KeyError, ImportError, RecursionError, subprocess.SubprocessError):
        print('{"code":"POST_VERDICT_RECOVERY_REJECTED"}')
        return 1


if __name__ == '__main__': raise SystemExit(main())

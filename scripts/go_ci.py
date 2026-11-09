"""Bounded Go checks. A passing regression does not certify product acceptance."""

import argparse
import hashlib
import ipaddress
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
MODULE = ROOT / 'backend'
GO_VERSION = 'go1.27.3'
PRODUCT_FILES = {
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount': {
        'close.go', 'lifecycle.go', 'model.go', 'plan.go', 'prepare.go', 'recovery.go', 'source.go', 'validation.go',
        'account_query.go', 'account_query_buffer.go', 'account_query_read.go',
        'journal.go', 'journal_codec.go', 'journal_security.go', 'sid_codec.go',
        'sid_run.go', 'sid_run_io.go', 'sid_script.go', 'sid_source.go',
        'creation_codec.go', 'creation_native.go', 'creation_query.go',
        'creation_script.go', 'creation_source.go', 'sid_script_parts.go',
        'permission_aliases.go', 'permission_aliases_buffer.go', 'permission_aliases_native.go',
        'permission_aliases_read.go', 'permission_creation.go', 'permission_facts.go',
        'permission_facts_buffer.go', 'permission_groups.go', 'permission_inspect.go',
        'permission_objects.go', 'permission_objects_buffer.go', 'permission_policy.go',
        'staging.go', 'staging_borrow.go', 'staging_native.go',
        'staging_profile.go', 'staging_query.go', 'never_created.go', 'never_created_session.go',
        'fixed_layout.go', 'fixed_layout_strict.go', 'fixed_layout_validate.go', 'fixed_layout_wire.go',
        'runtime_close.go', 'runtime_credential_io.go', 'runtime_credential_run.go', 'runtime_credential_script.go', 'runtime_expectation.go', 'runtime_guest.go', 'runtime_layout.go', 'runtime_native.go', 'runtime_ops.go', 'runtime_probe.go', 'runtime_rights.go', 'runtime_session.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend': {'activity.go', 'session.go', 'transport.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-backend': {'main.go', 'native.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-service-host': {'main.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-auth-helper': {'main.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/auth': {'keys.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/control': {'coordinator.go', 'codec.go', 'adapter.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/forward': {'policy.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease': {'authority.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio': {'loop.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/supervisor': {'authority.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral': {'issuer.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost': {
        'control.go', 'host.go', 'approved.go', 'job.go', 'ownercontrol.go', 'request_loop.go',
        'issued.go', 'nativebootstrap.go', 'nativestdio.go', 'ownedstdio.go',
        'scm_ready.go', 'service.go', 'outer.go', 'dispatch.go', 'dispatch_owner.go', 'reservation.go', 'delivery.go', 'auth_dispatch.go', 'workload_reclaim.go', 'auth_exchange.go', 'auth_exchange_endpoint.go', 'auth_exchange_worker.go', 'control_factory.go', 'control_readiness.go', 'control_exchange.go', 'preview.go', 'preview_dispatch.go', 'confirmation.go', 'confirmed_grant.go', 'confirmed_grant_snapshot.go', 'confirmed_target.go', 'guest_staging.go', 'guest_staging_dispatch.go', 'operator_sid.go', 'runtime_guest.go', 'runtime_guest_actor.go', 'runtime_guest_native.go', 'runtime_guest_prepare.go', 'runtime_guest_admission.go', 'runtime_guest_evaluation.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/localcontrol': {'policy.go', 'boundary.go', 'wire.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend': {'runtime.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob': {'child.go', 'plan.go', 'fixed_command.go', 'prepared.go', 'inspection.go', 'guest_token.go', 'guest_token_policy.go', 'guest_token_buffer.go', 'protected_guest.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey': {
        'acl.go', 'format.go', 'loader.go', 'profile.go', 'scope.go', 'source.go', 'validation.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap': {
        'codec.go', 'launch.go', 'native_receiver.go', 'strict.go', 'types.go', 'validate.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio': {'operation.go', 'pipes.go', 'streams.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapio': {'io.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth': {
        'auth.go', 'image.go', 'kernel.go', 'profile.go', 'validation.go', 'prepared_service.go', 'prepared_lease.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready': {
        'config.go', 'operation.go', 'plane.go', 'security.go', 'process_token.go', 'preflight.go', 'service_witness.go', 'endpoint.go', 'endpoint_input.go', 'endpoint_operation.go', 'endpoint_staging.go',
        'inspection_lease.go', 'inspection_lease_security.go', 'inspection_lease_token.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceprovision': {
        'compose.go', 'errors.go', 'record.go', 'record_decode.go', 'record_strict.go',
        'record_validate.go', 'root.go', 'root_doc.go', 'root_io.go', 'root_validate.go', 'service.go', 'preview.go', 'confirmation.go',
        'execution.go', 'execution_native.go', 'execution_owner.go', 'launch.go', 'launch_security.go', 'guest_source.go',
        'staging_execution.go', 'staging_preview.go', 'operator_policy.go', 'runtime_execution.go', 'runtime_native.go', 'runtime_source.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol': {'request.go', 'strict.go', 'response.go', 'response_fields.go', 'response_preview.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner': {'owner.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/fileread': {'read.go', 'access.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec': {'contract.go', 'output.go', 'run.go', 'session.go', 'local_summary.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel': {'channel.go', 'server.go', 'client.go', 'conn.go', 'keys.go', 'liveness.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire': {'client.go', 'server.go', 'strict.go', 'wire.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentwork': {'intent.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentclient': {'client.go', 'console.go', 'protocol.go', 'result.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/tether-work': {'main.go', 'entry.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/permissionmodel': {'model.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentapi': {'execution.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac': {'factory.go', 'standby.go', 'standby_types.go', 'wire.go', 'enrollment.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentpipe': {'codec.go', 'mapper.go', 'startup.go', 'foreground.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner': {'owner.go', 'run_once.go', 'wait_source.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink': {'host.go', 'client.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter': {'jsonrpc.go', 'server.go', 'tools.go', 'output_schema.go', 'http_bridge.go', 'resident_http.go', 'resident_output_schema.go', 'resident_list.go', 'resident_read_output.go', 'resident_get_execution.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway': {'gateway.go', 'http.go', 'token.go', 'config.go', 'resident.go', 'resident_capacity.go', 'resident_list.go', 'resident_read_output.go', 'resident_get_execution.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-gateway': {'main.go', 'config.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/tether-echo-probe': {'main.go', 'probe.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/tether-access-identity': {'main.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-mac-agent': {'main.go', 'config.go', 'status.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-mcp': {'main.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/tether-host': {'main.go', 'entry.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent': {'runtime.go', 'process.go', 'cleanup_diagnostic.go', 'groupobserver.go', 'start_diagnostic.go', 'terminal_disposition.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch': {'approval.go', 'resident_policy.go', 'resident_source.go', 'resident_wire.go', 'resident_worker.go', 'startup_policy.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport': {'protocol.go', 'stream.go', 'gateway.go', 'mac.go', 'resident_gateway.go', 'resident_handoff.go', 'resident_intent.go', 'resident_io.go', 'resident_mac.go', 'resident_protocol.go', 'resident_receiver.go', 'resident_rotation.go', 'resident_work.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys': {'loader.go', 'path.go', 'policy.go', 'acl.go', 'source.go', 'format.go', 'trust_config.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth': {
        'collector.go', 'controller.go', 'helper.go', 'types.go', 'launch.go', 'native_batch.go',
        'report.go', 'report_cleanup.go', 'report_endpoint.go', 'report_operation.go', 'report_owner.go', 'report_pair.go',
        'profile_frame.go', 'profile_stream.go', 'profile_sync.go', 'profile_pipe.go', 'profile_receiver.go',
        'helper_entry.go', 'helper_entry_owner.go', 'prepared_batch.go', 'batch_scope.go', 'profile_provider.go', 'profile_query.go', 'endpoint_candidate.go', 'sole_input_batch.go',
        'authenticated_report.go', 'operator_binding.go', 'sid_policy.go', 'sid_profile_frame.go',
        'supplier.go', 'supplier_config.go', 'helper_launch_source.go'},
}


GUEST_ACCOUNT_FILES = {
    'linux': {'account_query_other.go', 'creation_query_other.go', 'journal_other.go',
                'permission_aliases_other.go', 'permission_facts_other.go', 'permission_native_other.go',
                'permission_objects_other.go', 'staging_native_other.go', 'runtime_native_other.go'},
    'darwin': {'account_query_other.go', 'creation_query_other.go', 'journal_other.go',
                'permission_aliases_other.go', 'permission_facts_other.go', 'permission_native_other.go',
                'permission_objects_other.go', 'staging_native_other.go', 'runtime_native_other.go'},
    'windows': {'account_query_windows.go', 'account_query_windows_machine.go', 'creation_query_windows.go',
                'journal_windows.go', 'journal_windows_pin.go', 'permission_aliases_windows.go',
                'permission_facts_windows.go', 'permission_native_windows.go', 'permission_objects_windows.go',
                'permission_objects_windows_identity.go', 'staging_native_windows.go', 'runtime_native_windows.go', 'runtime_rights_windows.go', 'runtime_token_windows.go'},
}


SCM_FILES = {
    'linux': 'scm_other.go',
    'darwin': 'scm_other.go',
    'windows': 'scm_windows.go',
}


LOCALCONTROL_FILES = {
    'linux': {'native_other.go'},
    'darwin': {'native_other.go'},
    'windows': {'native_windows.go', 'io_windows.go'},
}


CHILDJOB_FILES = {
    'linux': {'native_other.go'},
    'darwin': {'native_other.go'},
    'windows': {'native_windows.go', 'artifact_windows.go', 'prepared_windows.go', 'guest_token_windows.go', 'protected_guest_windows.go'},
}


PROTECTEDKEY_FILES = {
    'linux': 'native_other.go',
    'darwin': 'native_other.go',
    'windows': 'native_windows.go',
}


NATIVE_BOOTSTRAP_FILES = {
    'linux': {
        'internal/bootstrap': {'native_receiver_other.go'},
        'internal/bootstrapio': {'native_other.go'},
        'internal/childstdio': {'native_other.go'},
        'internal/bootstrapauth': {'native_other.go'},
        'servicehost': {'nativebootstrap_other.go'},
    },
    'darwin': {
        'internal/bootstrap': {'native_receiver_other.go'},
        'internal/bootstrapio': {'native_other.go'},
        'internal/childstdio': {'native_other.go'},
        'internal/bootstrapauth': {'native_other.go'},
        'servicehost': {'nativebootstrap_other.go'},
    },
    'windows': {
        'internal/bootstrap': {'native_receiver_windows.go'},
        'internal/bootstrapio': {'native_windows.go'},
        'internal/childstdio': {'native_windows.go'},
        'internal/bootstrapauth': {'native_windows.go', 'image_windows.go'},
        'servicehost': {'nativebootstrap_windows.go'},
    },
}


SCM_READY_FILES = {
    'linux': {
        'internal/serviceready': {'native_other.go'},
        'servicehost': {'scm_ready_other.go'},
    },
    'darwin': {
        'internal/serviceready': {'native_other.go'},
        'servicehost': {'scm_ready_other.go'},
    },
    'windows': {
        'internal/serviceready': {'native_windows.go', 'process_token_windows.go', 'connect_windows.go', 'listener_windows.go', 'endpoint_windows.go', 'endpoint_io_windows.go', 'inspection_lease_identity_windows.go', 'inspection_lease_windows.go'},
        'servicehost': {'scm_ready_windows.go'},
    },
}


SERVICE_PROVISION_FILES = {
    'linux': {'root_other.go', 'launch_other.go'},
    'darwin': {'root_other.go', 'launch_other.go'},
    'windows': {'root_windows.go', 'root_windows_io.go', 'launch_windows.go',
                'launch_windows_security.go', 'launch_windows_threads.go'},
}


OPERATOR_AUTH_FILES = {
    'linux': {'native_other.go', 'native_batch_other.go', 'native_launch_other.go', 'native_report_other.go', 'profile_sync_other.go', 'profile_pipe_other.go', 'profile_receiver_other.go', 'helper_entry_other.go', 'profile_query_other.go', 'endpoint_profile_other.go',
              'helper_launch_native_other.go'},
    'darwin': {'native_other.go', 'native_batch_other.go', 'native_launch_other.go', 'native_report_other.go', 'profile_sync_other.go', 'profile_pipe_other.go', 'profile_receiver_other.go', 'helper_entry_other.go', 'profile_query_other.go', 'endpoint_profile_other.go',
               'helper_launch_native_other.go'},
    'windows': {'native_windows.go', 'native_token_windows.go', 'native_process_windows.go', 'native_batch_windows.go',
                'native_launch_binding_windows.go', 'native_launch_image_windows.go', 'native_launch_windows.go',
                'native_report_operation_windows.go', 'native_report_principal_windows.go', 'native_report_windows.go', 'profile_sync_windows.go', 'profile_pipe_windows.go', 'profile_receiver_windows.go', 'helper_entry_windows.go', 'profile_query_windows.go', 'endpoint_candidate_windows.go', 'endpoint_profile_windows.go',
                'supplier_image_windows.go', 'supplier_native_windows.go', 'supplier_security_windows.go', 'helper_launch_native_windows.go'},
}


MAC_AGENT_FILES = {
    'linux': {'native_other.go'},
    'darwin': {'native_darwin.go'},
    'windows': {'native_other.go'},
}


def product_files_for(goos, cgo_enabled=True):
    if type(cgo_enabled) is not bool:
        raise ValueError("CGO selection must be explicit boolean")
    if (goos not in SCM_FILES or goos not in LOCALCONTROL_FILES
            or goos not in CHILDJOB_FILES or goos not in PROTECTEDKEY_FILES
            or goos not in NATIVE_BOOTSTRAP_FILES or goos not in SCM_READY_FILES
            or goos not in SERVICE_PROVISION_FILES or goos not in OPERATOR_AUTH_FILES
            or goos not in GUEST_ACCOUNT_FILES or goos not in MAC_AGENT_FILES):
        raise ValueError('Unsupported Go target for production inventory')
    expected = {package: set(files) for package, files in PRODUCT_FILES.items()}
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/fileread'].update(
        {'selected_darwin.go', 'selected_observation_darwin.go'} if goos == 'darwin' else {'selected_other.go'})
    if goos == 'darwin' and cgo_enabled:
        expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentpipe'].update({'owned_darwin.go', 'run_darwin.go', 'foreground_darwin.go'})
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent'].update(MAC_AGENT_FILES[goos])
    if goos == 'darwin':
        expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent'].add('groupobserver_darwin.go' if cgo_enabled else 'groupobserver_darwin_nocgo.go')
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-mac-agent'].update({
        'status_darwin.go' if goos == 'darwin' else 'status_other.go',
        'status_clock_darwin.go' if goos == 'darwin' and cgo_enabled else 'status_clock_other.go'})
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch'].add('terminal_darwin.go' if goos == 'darwin' else 'terminal_other.go')
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac'].add(
        'enrollment_darwin.go' if goos == 'darwin' and cgo_enabled else 'enrollment_other.go')
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys'].add(
        'native_other.go' if goos == 'darwin' and not cgo_enabled else
        {'linux': 'native_linux.go', 'darwin': 'native_darwin.go', 'windows': 'native_windows.go'}[goos])
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount'].update(GUEST_ACCOUNT_FILES[goos])
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth'].update(OPERATOR_AUTH_FILES[goos])
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'].add(SCM_FILES[goos])
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/localcontrol'].update(LOCALCONTROL_FILES[goos])
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob'].update(CHILDJOB_FILES[goos])
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey'].add(PROTECTEDKEY_FILES[goos])
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceprovision'].update(SERVICE_PROVISION_FILES[goos])
    for suffix, files in NATIVE_BOOTSTRAP_FILES[goos].items():
        expected['github.com/O2-CONNECTIVE/rescue-host/backend/' + suffix].update(files)
    for suffix, files in SCM_READY_FILES[goos].items():
        expected['github.com/O2-CONNECTIVE/rescue-host/backend/' + suffix].update(files)
    return expected


def environment(download=False):
    env = dict(os.environ)
    env.update(GOTOOLCHAIN='local', GOWORK='off', GOENV='off', GOFLAGS='',
               GOPROXY='https://proxy.golang.org' if download else 'off',
               GOSUMDB='sum.golang.org', GOPRIVATE='', GONOSUMDB='',
               GONOPROXY='', CGO_ENABLED='1')
    # Keep the one-second child-exit measurement; remove only the race runtime's
    # documented extra one-second sleep after the program has already exited.
    env['GORACE'] = 'atexit_sleep_ms=0'
    for key in tuple(env):
        if key.startswith('RESCUE_'):
            del env[key]
    return env


def command(arguments, *, download=False, timeout=600):
    started = time.monotonic()
    completed = subprocess.run(
        ['go', *arguments], cwd=MODULE, env=environment(download),
        capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=timeout,
    )
    print(f'Go command {arguments[0]}: elapsed_s={time.monotonic() - started:.3f}, exit={completed.returncode}', flush=True)
    return completed


def require_success(result):
    if result.returncode:
        # Test inputs are public fixtures; strip the local checkout from diagnostics.
        detail = (result.stdout + result.stderr).replace(str(ROOT), '<repo>')
        print(detail[-12000:], file=sys.stderr)
        raise ValueError(f'Go command failed: exit={result.returncode}')


def module_hashes():
    return {name: hashlib.sha256((MODULE / name).read_bytes()).hexdigest()
            for name in ('go.mod', 'go.sum')}


def json_objects(source):
    decoder = json.JSONDecoder()
    while source.strip():
        source = source.lstrip()
        value, end = decoder.raw_decode(source)
        yield value
        source = source[end:]


def prepare():
    before = module_hashes()
    version = command(['version'])
    require_success(version)
    if version.stdout.split()[2] != GO_VERSION:
        raise ValueError('Unexpected Go version')
    print(version.stdout.strip(), flush=True)
    information = command(['env', '-json', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'CC'])
    require_success(information)
    metadata = json.loads(information.stdout)
    compiler = metadata['CC']
    metadata['CC'] = Path(compiler).name
    print('Environment:', json.dumps(metadata, sort_keys=True), flush=True)
    try:
        compiler_version = subprocess.run([compiler, '--version'], capture_output=True, text=True, timeout=30)
        print('Compiler:', compiler_version.stdout.splitlines()[0][:200], flush=True)
    except (OSError, IndexError) as error:
        print('Compiler unavailable:', type(error).__name__, flush=True)
    dependencies = command(['mod', 'download', '-json'], download=True)
    require_success(dependencies)
    require_success(command(['mod', 'verify'], download=True))
    if module_hashes() != before:
        raise ValueError('Dependency acquisition changed go.mod/go.sum')
    print('Module inputs unchanged:', json.dumps(before, sort_keys=True), flush=True)
    for module in json_objects(dependencies.stdout):
        if module.get('Replace'):
            raise ValueError('Unreviewed module replacement')
        print('Module:', module['Path'], module.get('Version', '(main)'), flush=True)


def build():
    target = command(['env', 'GOOS'])
    require_success(target)
    goos = target.stdout.strip()
    expected = product_files_for(goos)
    packages = command(['list', '-mod=readonly', '-json', './...'])
    require_success(packages)
    actual = {}
    for package in json_objects(packages.stdout):
        files = set(package.get('GoFiles', [])) | set(package.get('CgoFiles', []))
        actual[package['ImportPath']] = files
    if actual != expected:
        raise ValueError('Production file/package boundary changed')
    require_success(command(['vet', '-mod=readonly', './...']))
    require_success(command(['build', '-mod=readonly', '-trimpath', './...']))
    print(f'Vet/build: {len(expected)} packages; {sum(map(len, expected.values()))} production Go files; GOOS={goos}; no fixture files or tags', flush=True)


def test_inventory():
    result = command(['list', '-mod=readonly', '-json', './...'])
    require_success(result)
    expected = set()
    for package in json_objects(result.stdout):
        for name in package.get('TestGoFiles', []) + package.get('XTestGoFiles', []):
            path = Path(package['Dir']) / name
            if not path.resolve().is_relative_to(MODULE):
                raise ValueError('Test source outside public module')
            for test in re.findall(r'^func (Test\w+)\(', path.read_text(encoding='utf-8'), re.MULTILINE):
                if test != 'TestMain':
                    expected.add((package['ImportPath'], test))
    if not expected:
        raise ValueError('Empty Go test inventory')
    return expected


def test_events(source, returncode, expected_cpu=None, expected_tests=None):
    terminals = {}
    packages = {}
    started = set()
    errors = []
    outputs = {}
    cpus = set()
    for line in source.splitlines():
        event = json.loads(line)
        if not isinstance(event, dict):
            raise ValueError('Go event is not an object')
        package, test, action = event.get('Package'), event.get('Test'), event.get('Action')
        if not package:
            if event.get('Output'):
                outputs.setdefault((event.get('ImportPath', 'build'), None), []).append(event['Output'])
            continue
        if test:
            key = (package, test)
            if action == 'run':
                started.add(key)
            elif action in ('pass', 'skip', 'fail'):
                terminals[key] = action
            if action == 'skip':
                print('SKIP:', package, test, flush=True)
        elif action in ('pass', 'skip', 'fail'):
            packages[package] = action
        if action == 'output' and ('--- FAIL:' in event.get('Output', '') or
                                  'Error:' in event.get('Output', '') or
                                  '背圧' in event.get('Output', '')):
            errors.append(event['Output'].strip())
        if action == 'output':
            outputs.setdefault((package, test), []).append(event.get('Output', ''))
            cpus.update(int(value) for value in re.findall(r'observed_test_GOMAXPROCS=(\d+)', event.get('Output', '')))
    top = {key: value for key, value in terminals.items() if '/' not in key[1]}
    counts = {status: sum(value == status for value in top.values())
              for status in ('pass', 'skip', 'fail')}
    print('Go tests:', json.dumps(counts, sort_keys=True),
          'subtests=', len(terminals) - len(top), flush=True)
    for package, status in sorted(packages.items()):
        print('Package:', package, status, flush=True)
    for error in errors:
        print(error.replace(str(ROOT), '<repo>'), flush=True)
    for key, status in terminals.items():
        if status in ('fail', 'skip'):
            print(''.join(outputs.get(key, []))[-6000:].replace(str(ROOT), '<repo>'), flush=True)
    if returncode and not any(value == 'fail' for value in terminals.values()):
        for (package, test), lines in outputs.items():
            if not test:
                print(''.join(lines)[-6000:].replace(str(ROOT), '<repo>'), flush=True)
    if expected_cpu is not None:
        print('Observed test GOMAXPROCS:', sorted(cpus), flush=True)
        if cpus != {int(expected_cpu)}:
            raise ValueError('Requested CPU lane was not observed inside tests')
    if returncode or any(value == 'fail' for value in terminals.values()) or any(value == 'fail' for value in packages.values()):
        raise ValueError(f'Go tests failed: exit={returncode}')
    if not top or not counts['pass'] or started - terminals.keys():
        raise ValueError('Zero/all-skipped/incomplete Go tests')
    if any(value == 'skip' for value in terminals.values()):
        raise ValueError('Unexpected Go test skip (no test-level exclusions approved)')
    if set(packages) != set(PRODUCT_FILES):
        raise ValueError('Missing package test result')
    if expected_tests is not None and set(top) != expected_tests:
        raise ValueError('Selected Go tests did not match the public-source inventory')
    return counts


def check_network_record():
    path = MODULE / 'go-result.json'
    if not path.is_file():
        raise ValueError('Missing fixture resource record')
    record = json.loads(path.read_text(encoding='utf-8'))
    try:
        counters = {key: record.get(key) if type(record.get(key)) is int else 'invalid'
                    for key in ('test_exit_code', 'remaining_listeners', 'remaining_connections',
                                'forbidden_dial_attempts', 'duration_ms')}
        print('Fixture counters:', json.dumps(counters, sort_keys=True), flush=True)
        for key in ('test_exit_code', 'remaining_listeners', 'remaining_connections',
                    'forbidden_dial_attempts'):
            if counters[key] != 0:
                raise ValueError('Fixture cleanup/connection check failed: ' + key)
        for address in record.get('listener_addresses', []) + record.get('dial_addresses', []):
            host, port = address.rsplit(':', 1)
            if ipaddress.ip_address(host) != ipaddress.ip_address('127.0.0.1') or int(port) <= 1024:
                raise ValueError('Unexpected fixture address')
        print('Fixture resources: remaining=0; forbidden dials=0; owned high loopback only', flush=True)
    finally:
        path.unlink()


def tests(race=False, focused=False):
    record = MODULE / 'go-result.json'
    record.unlink(missing_ok=True)
    cpu = '2' if race else '1'
    inventory = None if focused else test_inventory()
    arguments = ['test', '-mod=readonly', '-trimpath', '-count=1', '-json',
                 '-p=1', '-parallel=1', '-cpu=' + cpu, '-timeout=5m' if race else '-timeout=2m']
    if race:
        arguments.append('-race')
    if focused:
        arguments.extend(['-run', '^TestChannelCloseHandshakeKeepsCapacity$'])
    arguments.append('./...')
    scope = 'focused channel lifecycle reproduction' if focused else 'full suite, no test exclusions'
    print(f'Lane: race={race}, requested CPU={cpu}; {scope}', flush=True)
    result = command(arguments, timeout=900 if race else 600)
    try:
        if result.stderr:
            print(result.stderr.replace(str(ROOT), '<repo>'), file=sys.stderr)
        failures = []
        try:
            test_events(result.stdout, result.returncode, expected_cpu=None if focused else cpu,
                        expected_tests=inventory)
        except ValueError as error:
            failures.append(str(error))
        try:
            check_network_record()
        except (OSError, ValueError) as error:
            failures.append(str(error))
        if failures:
            raise ValueError('; '.join(failures))
    finally:
        record.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('prepare', 'build', 'test', 'race', 'repro'))
    args = parser.parse_args()
    print('Acceptance: double-close NOT_MET (#5); Windows backpressure UNVERIFIED (#6).', flush=True)
    try:
        if not (MODULE / 'go.mod').is_file():
            raise ValueError('Missing public Go module')
        {'prepare': prepare, 'build': build, 'test': tests,
         'race': lambda: tests(True), 'repro': lambda: tests(focused=True)}[args.command]()
        return 0
    except (OSError, ValueError, KeyError, IndexError, subprocess.TimeoutExpired) as error:
        print('Go check failed:', str(error).replace(str(ROOT), '<repo>'), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())

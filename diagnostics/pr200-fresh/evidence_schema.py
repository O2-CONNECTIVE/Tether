"""Bounded evidence classifications. No discarded response/exception reconstruction."""
import report

JSON_SHAPES = {'not-observed', 'missing', 'null', 'string', 'number', 'boolean', 'object', 'array', 'other'}
SHAPE_FIELDS = {'envelopeShape', 'valueShape', 'capabilitiesShape', 'flatShape', 'nestedContainerShape', 'nestedShape'}
CANDIDATE_CLASSES = {'not-observed', 'envelope-not-object', 'value-not-object', 'capabilities-not-object',
    'candidate-missing', 'candidate-duplicated', 'candidate-not-string', 'candidate-invalid-path',
    'candidate-valid-flat', 'candidate-valid-nested', 'classification-error'}
CAPTURE_SITES = {'legacy-record-write', 'shape-record-write', 'request-extraction',
                 'response-registration', 'capability-classification', 'shape-classification'}
OBSERVER_SITES = {'platform-check', 'profile-path-validation', 'prelaunch-existence', 'owned-parent-path',
    'owned-parent-reparse', 'prelaunch-validation', 'baseline-snapshot', 'ready-marker-write',
    'snapshot-query', 'snapshot-limit', 'candidate-selection', 'candidate-count', 'driver-count',
    'lineage-query', 'webview-metadata', 'row-limit', 'process-handle-open', 'process-handle-identity',
    'argv-parse', 'profile-argument-check', 'observed-path-validation', 'actual-directory-existence',
    'actual-directory-reparse', 'actual-directory-canonical', 'requested-directory-existence',
    'requested-directory-reparse', 'row-record', 'process-handle-dispose', 'finish-marker-query',
    'poll-wait', 'observer-deadline', 'unclassified-site'}


def distinct_sites(value, allowed, limit):
    report.need(isinstance(value, list) and len(value) <= limit and all(
        isinstance(site, str) and site in allowed for site in value) and len(set(value)) == len(value))


def capability_shape_safe(value):
    report.need(isinstance(value, dict) and set(value) == SHAPE_FIELDS | {
        'schema', 'requests', 'settlements', 'candidateClass', 'failureSites'})
    report.need(type(value['schema']) is int and value['schema'] == 1)
    report.need(all(type(value[key]) is int and 0 <= value[key] <= 256 for key in ['requests', 'settlements']))
    report.need(all(isinstance(value[key], str) and value[key] in JSON_SHAPES for key in SHAPE_FIELDS))
    report.need(isinstance(value['candidateClass'], str) and value['candidateClass'] in CANDIDATE_CLASSES)
    distinct_sites(value['failureSites'], CAPTURE_SITES, 6)
    return value


def observer_sites_safe(value):
    report.need(isinstance(value, dict) and set(value) == {'schema', 'failureCount', 'sites', 'saturated', 'truncated'})
    report.need(type(value['schema']) is int and value['schema'] == 1)
    report.need(type(value['failureCount']) is int and 0 <= value['failureCount'] <= 4096)
    report.need(type(value['saturated']) is bool and type(value['truncated']) is bool)
    distinct_sites(value['sites'], OBSERVER_SITES, 16)
    report.need((value['failureCount'] == 0) == (not value['sites']))
    report.need(value['failureCount'] >= len(value['sites']))
    report.need(not value['saturated'] or value['failureCount'] == 4096)
    report.need(not value['truncated'] or len(value['sites']) == 16)
    return value


def classification_complete(diagnostics, profile, capability):
    if not isinstance(profile, dict) or not isinstance(capability, dict):
        return False
    if not isinstance(diagnostics, dict) or set(diagnostics) != {'profile-capability-shape.json', 'observer-failure-sites.json'}:
        return False
    try:
        shape = capability_shape_safe(diagnostics['profile-capability-shape.json'])
        sites = observer_sites_safe(diagnostics['observer-failure-sites.json'])
    except (ValueError, KeyError, TypeError):
        return False
    if (shape['requests'], shape['settlements']) != (capability.get('requests'), capability.get('settlements')):
        return False
    if shape['candidateClass'] in {'not-observed', 'classification-error'} or shape['failureSites']:
        return False
    if any(shape[key] in {'not-observed', 'other'} for key in SHAPE_FIELDS):
        return False
    if not shape_consistent(shape):
        return False
    valid_candidate = shape['candidateClass'] in {'candidate-valid-flat', 'candidate-valid-nested'}
    if capability.get('capabilityObserved') is not valid_candidate or capability.get('captureFailed') is not False:
        return False
    if valid_candidate and (any(shape[key] != 'object' for key in ['envelopeShape', 'valueShape', 'capabilitiesShape']) or
                            shape['flatShape' if shape['candidateClass'] == 'candidate-valid-flat' else 'nestedShape'] != 'string'):
        return False
    if sites['saturated'] or sites['truncated'] or 'unclassified-site' in sites['sites']:
        return False
    return profile.get('queryComplete') is (sites['failureCount'] == 0)


def shape_consistent(shape):
    """Reject contradictory type/class evidence; never recover raw candidate values."""
    code = shape['candidateClass']
    envelope, value, caps = (shape[key] for key in ['envelopeShape', 'valueShape', 'capabilitiesShape'])
    if code == 'envelope-not-object':
        return envelope != 'object'
    if code == 'value-not-object':
        return envelope == 'object' and value != 'object'
    if code == 'capabilities-not-object':
        return envelope == value == 'object' and caps != 'object'
    if envelope != 'object' or value != 'object' or caps != 'object':
        return False
    flat, nested = shape['flatShape'] != 'missing', shape['nestedShape'] != 'missing'
    if nested and shape['nestedContainerShape'] != 'object':
        return False
    if code == 'candidate-missing':
        return not flat and not nested
    if code == 'candidate-duplicated':
        return flat and nested
    if flat == nested:
        return False
    candidate_type = shape['flatShape'] if flat else shape['nestedShape']
    if code == 'candidate-not-string':
        return candidate_type != 'string'
    if code == 'candidate-invalid-path':
        return candidate_type == 'string'
    return candidate_type == 'string' and code == ('candidate-valid-flat' if flat else 'candidate-valid-nested')


def retained_classification(record):
    """The retained phase2 record has no sidecars. Unknown stays unknown."""
    if not isinstance(record, dict) or not isinstance(record.get('files', {}), dict):
        return {'capabilityClass': 'invalid-classification', 'observerSites': ['invalid-classification']}
    files = record.get('files', {})
    result = {'capabilityClass': 'not-retained', 'observerSites': ['not-retained']}
    if 'profile-capability-shape.json' in files:
        try:
            result['capabilityClass'] = capability_shape_safe(files['profile-capability-shape.json'])['candidateClass']
        except (ValueError, KeyError, TypeError):
            result['capabilityClass'] = 'invalid-classification'
    if 'observer-failure-sites.json' in files:
        try:
            result['observerSites'] = list(observer_sites_safe(files['observer-failure-sites.json'])['sites'])
        except (ValueError, KeyError, TypeError):
            result['observerSites'] = ['invalid-classification']
    return result

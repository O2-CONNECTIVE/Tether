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

PATH_PREDICATES = {'not-observed', 'not-applicable', 'accepted', 'non-string', 'drive-prefix',
    'forbidden-character', 'extra-colon', 'empty-component', 'dot-component', 'trailing-dot-or-space',
    'classification-error'}
PATH_REJECTIONS = PATH_PREDICATES - {'not-observed', 'not-applicable', 'accepted', 'non-string', 'classification-error'}
METADATA_DETAILS = {'metadata-creation-check-exception', 'metadata-creation-guard',
    'metadata-command-line-read-exception', 'metadata-command-line-guard', 'metadata-identity-key-exception',
    'metadata-tracking-check-exception'}
LINEAGE_DETAILS = {'lineage-parent-row-guard-site', 'lineage-parent-creation-guard-site',
    'lineage-child-row-guard-site', 'lineage-child-creation-guard-site', 'lineage-child-order-guard-site',
    'lineage-driver-live-check-site', 'lineage-edge-live-check-site', 'lineage-reference-check-site',
    'lineage-driver-descendants-call-site', 'lineage-app-selection-site', 'lineage-app-count-check-site',
    'lineage-app-descendants-call-site', 'lineage-webview-selection-site', 'lineage-webview-count-check-site',
    'lineage-root-selection-site', 'lineage-root-count-check-site', 'lineage-root-creation-check-site',
    'lineage-child-selection-site', 'lineage-child-identity-check-site', 'lineage-child-creation-check-site',
    'lineage-child-order-check-site'}
OBSERVER_DETAILS = METADATA_DETAILS | LINEAGE_DETAILS | {'operation-exception', 'unclassified-detail'}


def detail_allowed(site, code):
    if code == 'unclassified-detail':
        return True
    if site == 'lineage-query':
        return code in LINEAGE_DETAILS
    if site == 'webview-metadata':
        return code in METADATA_DETAILS
    return site != 'unclassified-site' and code == 'operation-exception'


def distinct_sites(value, allowed, limit):
    report.need(isinstance(value, list) and len(value) <= limit and all(
        isinstance(site, str) and site in allowed for site in value) and len(set(value)) == len(value))


def capability_shape_safe(value):
    report.need(isinstance(value, dict) and type(value.get('schema')) is int and value['schema'] in {1, 2})
    fields = SHAPE_FIELDS | {'schema', 'requests', 'settlements', 'candidateClass', 'failureSites'}
    report.need(set(value) == (fields | {'pathPredicate'} if value['schema'] == 2 else fields))
    if value['schema'] == 2:
        report.need(isinstance(value['pathPredicate'], str) and value['pathPredicate'] in PATH_PREDICATES)
    report.need(all(type(value[key]) is int and 0 <= value[key] <= 256 for key in ['requests', 'settlements']))
    report.need(all(isinstance(value[key], str) and value[key] in JSON_SHAPES for key in SHAPE_FIELDS))
    report.need(isinstance(value['candidateClass'], str) and value['candidateClass'] in CANDIDATE_CLASSES)
    distinct_sites(value['failureSites'], CAPTURE_SITES, 6)
    return value


def observer_sites_safe(value):
    report.need(isinstance(value, dict) and type(value.get('schema')) is int and value['schema'] in {1, 2})
    fields = {'schema', 'failureCount', 'sites', 'saturated', 'truncated'}
    report.need(set(value) == (fields | {'details'} if value['schema'] == 2 else fields))
    report.need(type(value['failureCount']) is int and 0 <= value['failureCount'] <= 4096)
    report.need(type(value['saturated']) is bool and type(value['truncated']) is bool)
    distinct_sites(value['sites'], OBSERVER_SITES, 16)
    report.need((value['failureCount'] == 0) == (not value['sites']))
    report.need(value['failureCount'] >= len(value['sites']))
    report.need(not value['saturated'] or value['failureCount'] == 4096)
    if value['schema'] == 2:
        details = value['details']
        report.need(isinstance(details, list) and len(details) <= 16)
        pairs = []
        for detail in details:
            report.need(isinstance(detail, dict) and set(detail) == {'site', 'code'})
            site, code = detail['site'], detail['code']
            report.need(isinstance(site, str) and site in value['sites'] and isinstance(code, str) and
                        code in OBSERVER_DETAILS and detail_allowed(site, code))
            pairs.append((site, code))
        report.need(len(set(pairs)) == len(pairs) and value['failureCount'] >= len(pairs))
        report.need((value['failureCount'] == 0) == (not pairs))
        report.need(value['truncated'] or {site for site, _ in pairs} == set(value['sites']))
        report.need(not value['truncated'] or len(value['sites']) == 16 or len(details) == 16)
    else:
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
    # Old retained classifications remain readable, never gain absent predicates/details.
    if shape['schema'] != 2 or sites['schema'] != 2:
        return False
    if not predicate_consistent(shape) or any(detail['code'] == 'unclassified-detail' for detail in sites['details']):
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


def predicate_consistent(shape):
    code, predicate = shape['candidateClass'], shape['pathPredicate']
    if code == 'candidate-invalid-path':
        return predicate in PATH_REJECTIONS
    if code in {'candidate-valid-flat', 'candidate-valid-nested'}:
        return predicate == 'accepted'
    if code == 'not-observed':
        return predicate == 'not-observed'
    if code == 'classification-error':
        return predicate == 'classification-error'
    return predicate == 'not-applicable'


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
        return {'capabilityClass': 'invalid-classification', 'observerSites': ['invalid-classification'],
                'pathPredicate': 'not-retained', 'observerDetails': 'not-retained'}
    files = record.get('files', {})
    result = {'capabilityClass': 'not-retained', 'observerSites': ['not-retained'],
              'pathPredicate': 'not-retained', 'observerDetails': 'not-retained'}
    if 'profile-capability-shape.json' in files:
        try:
            shape = capability_shape_safe(files['profile-capability-shape.json'])
            result['capabilityClass'] = shape['candidateClass']
            if shape['schema'] == 2:
                result['pathPredicate'] = shape['pathPredicate']
        except (ValueError, KeyError, TypeError):
            result['capabilityClass'] = 'invalid-classification'
    if 'observer-failure-sites.json' in files:
        try:
            sites = observer_sites_safe(files['observer-failure-sites.json'])
            result['observerSites'] = list(sites['sites'])
            if sites['schema'] == 2:
                result['observerDetails'] = [dict(detail) for detail in sites['details']]
        except (ValueError, KeyError, TypeError):
            result['observerSites'] = ['invalid-classification']
    return result

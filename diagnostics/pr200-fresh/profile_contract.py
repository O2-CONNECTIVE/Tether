"""Source-only contract for phase2 profile evidence. Never launches a process."""
import ntpath
import re


def canonical_candidate(value):
    """Conservative local absolute Windows path; reject aliases needing OS resolution."""
    if not isinstance(value, str) or not re.fullmatch(r'[A-Za-z]:[\\/].+', value):
        raise ValueError('PROFILE_PATH_REJECTED')
    if any(character in value for character in '\x00\r\n"*?<>|'):
        raise ValueError('PROFILE_PATH_REJECTED')
    path = value.replace('/', '\\')
    if ':' in path[2:] or any(part in {'', '.', '..'} or part.endswith((' ', '.'))
                            for part in path[3:].split('\\')):
        raise ValueError('PROFILE_PATH_REJECTED')
    return ntpath.normcase(path)


def profile_argument(argv):
    """Input is argv from Windows CommandLineToArgvW, never a regex on raw text."""
    if not isinstance(argv, list) or not 1 <= len(argv) <= 256 or not all(isinstance(x, str) for x in argv):
        raise ValueError('PROFILE_ARGUMENT_REJECTED')
    matches = [value[len('--user-data-dir='):] for value in argv
               if value.startswith('--user-data-dir=')]
    # Split flags and duplicates are unknown, not an assumed default directory.
    if len(matches) != 1 or '--user-data-dir' in argv:
        raise ValueError('PROFILE_ARGUMENT_REJECTED')
    return canonical_candidate(matches[0])


def classify(requested, argv):
    """Allow only the exact fresh root or one explicit private child, never a prefix."""
    root = canonical_candidate(requested)
    observed = profile_argument(argv)
    if observed == root:
        return 'requested-root'
    if observed == ntpath.join(root, 'ebwebview'):
        return 'requested-child-ebwebview'
    return 'outside-requested-profile'


def accepted(evidence):
    """All native/FS evidence is mandatory. Missing, false, empty, partial stops jobs."""
    if not isinstance(evidence, dict) or set(evidence) != {
        'schema', 'prelaunchAbsent', 'parentOwned', 'parentReparseFree', 'queryComplete',
        'lineageConfirmed', 'allObservedWebviewsCovered', 'rows', 'omittedRows'
    }:
        return False
    if type(evidence['schema']) is not int or evidence['schema'] != 1:
        return False
    if not all(evidence[key] is True for key in ['prelaunchAbsent', 'parentOwned',
        'parentReparseFree', 'queryComplete', 'lineageConfirmed', 'allObservedWebviewsCovered']):
        return False
    rows = evidence['rows']
    if not isinstance(rows, list) or not 1 <= len(rows) <= 32 or type(evidence['omittedRows']) is not int or evidence['omittedRows'] != 0:
        return False
    for row in rows:
        if not isinstance(row, dict) or set(row) != {
            'identityVerified', 'mapping', 'actualDirectoryExists', 'actualPathReparseFree',
            'actualCanonicalPathMatches', 'requestedDirectoryCreated', 'requestedPathReparseFree'
        }:
            return False
        # A strict child is a useful diagnostic classification, but the unchanged
        # supervisor does not prove that child for its exact owned collection.
        if row['mapping'] != 'requested-root':
            return False
        if not all(row[key] is True for key in row if key != 'mapping'):
            return False
    # Two accepted lexical forms still cannot silently describe different profiles.
    return len({row['mapping'] for row in rows}) == 1


def capability_accepted(value, process_evidence):
    fields = {'schema', 'requests', 'settlements', 'requestedMatchesEnvironment',
              'requestedAbsentBeforeRequest', 'capabilityObserved', 'capabilityMatchesRequested',
              'capabilityMapping', 'captureFailed'}
    if not isinstance(value, dict) or set(value) != fields or not accepted(process_evidence):
        return False
    if any(type(value[key]) is not int or value[key] != 1 for key in ['schema', 'requests', 'settlements']):
        return False
    if not all(value[key] is True for key in ['requestedMatchesEnvironment', 'requestedAbsentBeforeRequest', 'capabilityObserved']):
        return False
    if value['captureFailed'] is not False:
        return False
    mapping = value['capabilityMapping']
    if mapping == 'requested-root':
        return value['capabilityMatchesRequested'] is True
    return False

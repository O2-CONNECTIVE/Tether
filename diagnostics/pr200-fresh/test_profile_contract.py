"""Synthetic path/argv/evidence fixtures only. No Windows execution evidence."""
import unittest
import profile_contract as contract


def valid():
    return {'schema': 1, 'prelaunchAbsent': True, 'parentOwned': True,
        'parentReparseFree': True, 'queryComplete': True, 'lineageConfirmed': True,
        'allObservedWebviewsCovered': True, 'omittedRows': 0,
        'rows': [{'identityVerified': True, 'mapping': 'requested-root',
            'actualDirectoryExists': True, 'actualPathReparseFree': True,
            'actualCanonicalPathMatches': True, 'requestedDirectoryCreated': True,
            'requestedPathReparseFree': True}]}


class ProfileContractTests(unittest.TestCase):
    def test_exact_root_and_explicit_child_are_distinct(self):
        root = r'C:\owned trial\profile'
        self.assertEqual(contract.classify(root, ['webview.exe', '--user-data-dir=' + root]), 'requested-root')
        self.assertEqual(contract.classify(root, ['webview.exe', '--user-data-dir=C:/OWNED TRIAL/profile/EBWebView']),
                         'requested-child-ebwebview')

    def test_sibling_prefix_nested_child_and_unrelated_directory_fail(self):
        root = r'C:\owned\profile'
        for path in [root + 'suffix', root + r'\EBWebView\nested', root + r'\other', r'C:\default\profile']:
            self.assertEqual(contract.classify(root, ['webview.exe', '--user-data-dir=' + path]), 'outside-requested-profile')

    def test_missing_duplicate_split_or_quoted_suffix_arguments_reject(self):
        root = r'C:\owned\profile'
        for argv in [[], ['webview.exe'], ['webview.exe', '--user-data-dir', root],
                     ['webview.exe', '--user-data-dir=' + root, '--user-data-dir=' + root],
                     ['webview.exe', '--user-data-dir="' + root + '"suffix']]:
            with self.assertRaises(ValueError):
                contract.classify(root, argv)

    def test_ambiguous_os_alias_paths_reject(self):
        for value in [r'\\server\share\profile', r'\\?\C:\owned\profile', r'C:relative',
                      r'C:\owned\..\profile', r'C:\owned\.\profile', 'C:\\owned\\profile.',
                      'C:\\owned\\profile ', r'C:\owned\profile:stream', r'C:\owned\\profile']:
            with self.assertRaises(ValueError):
                contract.canonical_candidate(value)

    def test_every_native_gate_is_required_and_no_empty_success(self):
        self.assertTrue(contract.accepted(valid()))
        for key in ['prelaunchAbsent', 'parentOwned', 'parentReparseFree', 'queryComplete',
                    'lineageConfirmed', 'allObservedWebviewsCovered']:
            for value in [False, None, 1]:
                data = valid(); data[key] = value
                self.assertFalse(contract.accepted(data))
            data = valid(); del data[key]
            self.assertFalse(contract.accepted(data))
        for key in valid()['rows'][0]:
            if key == 'mapping': continue
            data = valid(); data['rows'][0][key] = False
            self.assertFalse(contract.accepted(data))
        for rows in [[], [dict(valid()['rows'][0], mapping='outside-requested-profile')],
                     [valid()['rows'][0], dict(valid()['rows'][0], mapping='requested-child-ebwebview')]]:
            data = valid(); data['rows'] = rows
            self.assertFalse(contract.accepted(data))
        data = valid(); data['omittedRows'] = 1
        self.assertFalse(contract.accepted(data))


if __name__ == '__main__':
    unittest.main()

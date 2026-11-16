"""Check and conservatively repair Tether issue triage labels."""
import argparse
import json
import os
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urljoin, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    # 転送先へ token を渡さず、POST/DELETE が GET に化けて成功扱いになるのも防ぐ。
    def redirect_request(self, *args, **kwargs):
        return None


OPENER = build_opener(NoRedirect)

AREAS = {'Resident', 'Gateway-MCP', 'Guest-Isolation', 'Tauri-UI', 'Distribution', 'Testing', 'Dev-Process', 'Product'}
KINDS = {'Bug', 'Feature', 'Refactor', 'Test', 'Perf', 'Security', 'Docs', 'Ops', 'Decision'}
EXCLUDED = {'Invalid', 'Duplicate', 'Wontfix'}
SUBTYPES = {'HITL:Decision', 'HITL:Manual-Action', 'HITL:Research', 'HITL:Verification'}


def evaluate(state, labels):
    if state not in {'open', 'closed'} or not isinstance(labels, list) or not all(isinstance(x, str) for x in labels):
        raise ValueError('invalid issue state or labels')
    names = set(labels)
    areas = [x for x in labels if x.startswith('Area:') and x[5:] in AREAS]
    kinds = [x for x in labels if x.startswith('Kind:') and x[5:] in KINDS]
    excluded = bool(names & EXCLUDED)
    classified = excluded or (len(areas) == 1 and len(kinds) == 1)
    add, remove, violations = [], [], []
    if classified and 'Needs-Category' in names:
        remove.append('Needs-Category')
    elif not classified and 'Needs-Category' not in names:
        add.append('Needs-Category')
    if state == 'closed':
        return {'add': add, 'remove': remove, 'violations': violations}

    subtypes = names & SUBTYPES
    hitl = 'HITL' in names
    effective_hitl = hitl or bool(subtypes)
    afk = 'AFK' in names
    # 人が残す側を選ぶ組合せでは、片側を補強しない。
    lifecycle = bool(names & {'Needs-Triage', 'Needs-Info'})
    if subtypes and not hitl and not afk and not lifecycle:
        add.append('HITL')
    if afk and effective_hitl:
        violations.append('destination.conflict')
    if afk and any(label.startswith('Blocked:') for label in names):
        violations.append('destination.blocked')
    if hitl and not subtypes:
        violations.append('hitl.subtype-missing')
    if 'Needs-Triage' in names and (afk or effective_hitl):
        violations.append('lifecycle.needs-triage-with-destination')
    if 'Needs-Info' in names and (afk or effective_hitl):
        violations.append('lifecycle.needs-info-with-destination')
    return {'add': add, 'remove': remove, 'violations': violations}


class GitHub:
    def __init__(self):
        self.token = os.environ['GITHUB_TOKEN']
        self.repository = os.environ['GITHUB_REPOSITORY']
        self.base = 'https://api.github.com/repos/' + self.repository

    def request(self, path, method='GET', body=None, missing_ok=False):
        headers = {'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28',
                   'Authorization': 'Bearer ' + self.token}
        data = None
        if body is not None:
            headers['Content-Type'] = 'application/json'
            data = json.dumps(body).encode('utf-8')
        # GitHub の次ページ URL は /repositories/{id}/... の絶対 URL なので、そのまま使う。
        url = path if path.startswith('https://api.github.com/') else self.base + path
        request = Request(url, data=data, headers=headers, method=method)
        try:
            with OPENER.open(request, timeout=30) as response:
                payload = response.read()
                return json.loads(payload) if payload else None, response.headers.get('Link', '')
        except HTTPError as error:
            if missing_ok and error.code == 404:
                return None, ''
            print('GitHub API status {}'.format(error.code), file=sys.stderr)
            raise RuntimeError('GitHub API request failed') from None
        except (URLError, OSError, ValueError):
            raise RuntimeError('GitHub API request failed') from None

    def pages(self, path):
        url = self.base + path
        while url:
            parts = urlsplit(url)
            if parts.scheme != 'https' or parts.netloc != 'api.github.com':
                raise RuntimeError('GitHub API pagination failed')
            result, link = self.request(url)
            yield result
            url = ''
            for item in link.split(','):
                if 'rel="next"' in item:
                    url = urljoin(self.base, item.split(';', 1)[0].strip().strip('<>'))
                    break


def labels(issue):
    return [item.get('name', '') for item in issue.get('labels', []) if isinstance(item, dict)]


def summary(rows):
    filename = os.environ.get('GITHUB_STEP_SUMMARY')
    if not filename:
        return
    with open(filename, 'a', encoding='utf-8') as stream:
        stream.write('\n| number | auto-fix | violations |\n| --- | --- | --- |\n')
        for number, result in rows:
            fix = ', '.join(['add ' + x for x in result['add']] + ['remove ' + x for x in result['remove']])
            stream.write('| {} | {} | {} |\n'.format(number, fix, ', '.join(result['violations'])))


def issue_mode(number):
    api = GitHub()
    issue, _ = api.request('/issues/' + str(number))
    if 'pull_request' in issue:
        return 0
    result = evaluate(issue['state'], labels(issue))
    path = '/issues/' + str(number) + '/labels'
    if result['add']:
        api.request(path, 'POST', {'labels': result['add']})
    for label in result['remove']:
        # 人が同時に外していた場合の 404 は、目的の状態なので成功とする。
        api.request(path + '/' + quote(label, safe=''), 'DELETE', missing_ok=True)
    if result['violations']:
        summary([(number, result)])
        return 1
    return 0


def audit_mode():
    api = GitHub()
    rows = []
    for page in api.pages('/issues?' + urlencode({'state': 'all', 'per_page': 100})):
        for item in page:
            if 'pull_request' not in item:
                result = evaluate(item['state'], labels(item))
                if result['add'] or result['remove'] or result['violations']:
                    rows.append((item['number'], result))
    summary(rows)
    return int(bool(rows))


def main():
    parser = argparse.ArgumentParser()
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument('--evaluate', action='store_true')
    modes.add_argument('--issue', type=int)
    modes.add_argument('--audit', action='store_true')
    parser.add_argument('--state', choices=('open', 'closed'))
    parser.add_argument('--label', action='append', default=[])
    args = parser.parse_args()
    try:
        if args.evaluate:
            if args.state is None:
                parser.error('--evaluate requires --state')
            result = evaluate(args.state, args.label)
            print(json.dumps(result, ensure_ascii=False, separators=(',', ':')))
            return int(bool(result['violations']))
        if args.state is not None or args.label:
            parser.error('--state and --label are only valid with --evaluate')
        return issue_mode(args.issue) if args.issue is not None else audit_mode()
    except (KeyError, RuntimeError, ValueError, TypeError, OSError):
        print('issue triage operation failed', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())

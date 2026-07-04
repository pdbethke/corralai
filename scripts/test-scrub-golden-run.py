#!/usr/bin/env python3
# SPDX-License-Identifier: Elastic-2.0
"""scripts/test-scrub-golden-run.py — unit tests for the golden-run privacy gate.

Run with: pytest scripts/test-scrub-golden-run.py -v
(scrub-golden-run.py has a hyphenated name to match the export script's
`python3 scripts/scrub-golden-run.py ...` invocation, so it's loaded here
via importlib rather than a normal `import` statement.)

Every deny-list class gets a planted violation here, plus a matching
"this must NOT trip" control where relevant (RFC1918 IPs, demo-container
paths). The manifest tests pin the shape the export script's human-review
step and Task 4's consumers rely on: sorted, deduped paths/URLs/actors.
"""
import importlib.util
import io
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stdout

_MODULE_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'scrub-golden-run.py')
_spec = importlib.util.spec_from_file_location('scrub_golden_run', _MODULE_PATH)
scrub = importlib.util.module_from_spec(_spec)
sys.modules['scrub_golden_run'] = scrub
_spec.loader.exec_module(scrub)


def _write(text):
    fd, path = tempfile.mkstemp(suffix='.json')
    with os.fdopen(fd, 'w', encoding='utf-8') as f:
        f.write(text)
    return path


class DenyListPlantedViolations(unittest.TestCase):
    """One planted violation per deny class — each must be caught."""

    def _assert_caught(self, text, expected_label_substr, expected_snippet):
        offenses = scrub.scan_deny(text, '', '')
        labels = [label for label, _ in offenses]
        self.assertTrue(
            any(expected_label_substr in label for label in labels),
            f'expected a "{expected_label_substr}" offense in {offenses}',
        )
        snippets = [snip for _, snip in offenses]
        self.assertIn(expected_snippet, snippets)

    def test_email_address(self):
        self._assert_caught(
            'contact pat@example.com for help', 'email address', 'pat@example.com'
        )

    def test_linux_home_path(self):
        self._assert_caught(
            'file at /home/pat/project/main.go', 'linux home-directory path', '/home/pat'
        )

    def test_macos_home_path(self):
        self._assert_caught(
            'file at /Users/pat/project/main.go', 'macOS home-directory path', '/Users/pat'
        )

    def test_github_token(self):
        token = 'ghp_' + 'a' * 36
        self._assert_caught(f'token={token}', 'GitHub token', token)

    def test_aws_access_key(self):
        key = 'AKIA' + 'A' * 16
        self._assert_caught(f'key={key}', 'AWS access key id', key)

    def test_vendor_token(self):
        token = 'cdt_' + 'b' * 24
        self._assert_caught(f'secret={token}', 'vendor token', token)

    def test_openai_shaped_key(self):
        key = 'sk-' + 'c' * 24
        self._assert_caught(f'apikey={key}', 'OpenAI-shaped API key', key)

    def test_private_key_marker(self):
        marker = '-----BEGIN RSA PRIVATE KEY-----'
        self._assert_caught(f'blob={marker}', 'private key material', marker)

    def test_operator_username(self):
        offenses = scrub.scan_deny('logged in as myuser today', 'myuser', '')
        self.assertTrue(
            any('username' in label for label, _ in offenses),
            f'expected username offense in {offenses}',
        )

    def test_operator_hostname(self):
        offenses = scrub.scan_deny('running on myhost.local now', '', 'myhost.local')
        self.assertTrue(
            any('hostname' in label for label, _ in offenses),
            f'expected hostname offense in {offenses}',
        )

    def test_non_private_ip(self):
        self._assert_caught('reached 8.8.8.8 over the wire', 'non-private', '8.8.8.8')

    def test_absolute_path_outside_demo_roots(self):
        self._assert_caught(
            'wrote to /etc/some/config/file', 'absolute path outside demo-container roots',
            '/etc/some/config/file',
        )

    def test_windows_drive_letter_path_plain(self):
        # Anything drive-lettered in a Linux-container demo export is
        # suspicious enough to deny outright.
        self._assert_caught(
            r'saved to C:\Users\pat\proj today', 'Windows drive-letter path',
            r'C:\Users\pat\proj',
        )

    def test_windows_drive_letter_path_json_escaped(self):
        # Inside raw JSON text backslashes arrive doubled — must still trip.
        text = '{"note":"saved to C:\\\\Users\\\\pat\\\\proj"}'
        offenses = scrub.scan_deny(text, '', '')
        self.assertTrue(
            any('Windows drive-letter path' in label for label, _ in offenses),
            f'JSON-escaped drive path missed: {offenses}',
        )

    def test_unc_backslash_users_path(self):
        # Drive-letter-less backslash paths through Users/home segments are
        # host-identifying too.
        self._assert_caught(
            r'copied \Users\pat\stuff over', 'Windows backslash home path',
            r'\Users\pat',
        )


class DenyListControls(unittest.TestCase):
    """Things that must NOT trip the deny-list."""

    def test_rfc1918_ip_is_clean(self):
        offenses = scrub.scan_deny('host 10.0.0.5 answered', '', '')
        self.assertFalse(any('IP' in label for label, _ in offenses))

    def test_loopback_ip_is_clean(self):
        offenses = scrub.scan_deny('bound to 127.0.0.1', '', '')
        self.assertFalse(any('IP' in label for label, _ in offenses))

    def test_demo_container_paths_are_clean(self):
        for root in ('/work/repo/main.go', '/tmp/scratch/file', '/root/.cache/x'):
            offenses = scrub.scan_deny(f'path {root} touched', '', '')
            self.assertFalse(
                any('absolute path' in label for label, _ in offenses),
                f'{root} should be a safe demo-container path',
            )

    def test_non_ip_dotted_string_is_clean(self):
        # e.g. a Go version string shaped like an IP but not a real address
        offenses = scrub.scan_deny('built with go1.26.4 toolchain', '', '')
        self.assertFalse(any('IP' in label for label, _ in offenses))

    def test_empty_whoami_hostname_no_false_positive(self):
        # blank whoami/hostname must not match everything via re.escape('')
        offenses = scrub.scan_deny('a perfectly ordinary sentence', '', '')
        self.assertEqual(offenses, [])

    def test_go_module_path_is_not_an_absolute_path(self):
        # A domain-prefixed module path (github.com/yourusername/stack) is not
        # a host filesystem path — the /yourusername/... tail must not trip
        # the absolute-path deny. (It still shows up in the manifest for the
        # human to eyeball.) Seen in the wild: mission 2's builder ran
        # `go test github.com/yourusername/stack`.
        offenses = scrub.scan_deny(
            'ran go test github.com/yourusername/stack/test', '', ''
        )
        self.assertFalse(
            any('absolute path' in label for label, _ in offenses),
            f'module path wrongly flagged as absolute: {offenses}',
        )

    def test_absolute_path_after_quote_still_caught(self):
        # JSON string values start with a quote — the deny must still fire there.
        offenses = scrub.scan_deny('{"note":"/etc/passwd/x"}', '', '')
        self.assertTrue(
            any('absolute path' in label for label, _ in offenses),
            f'quoted absolute path missed: {offenses}',
        )

    def test_absolute_path_after_equals_still_caught(self):
        offenses = scrub.scan_deny('dir=/opt/secret/place run', '', '')
        self.assertTrue(
            any('absolute path' in label for label, _ in offenses),
            f'=-prefixed absolute path missed: {offenses}',
        )


class DenyCommandBehavior(unittest.TestCase):
    def test_dirty_fixture_exits_nonzero_and_prints_offenses(self):
        path = _write(json.dumps({
            'events': [{
                'ts': 1, 'kind': 'x', 'actor': 'a',
                'detail': {'note': 'contact pat@example.com at /home/pat/proj'},
            }]
        }))
        try:
            buf = io.StringIO()
            with redirect_stdout(buf):
                with self.assertRaises(SystemExit) as cm:
                    scrub.cmd_deny(path, 'myuser', 'myhost')
            self.assertEqual(cm.exception.code, 1)
        finally:
            os.remove(path)

    def test_clean_fixture_exits_zero(self):
        path = _write(json.dumps({
            'events': [{
                'ts': 1, 'kind': 'execution', 'actor': 'builder-1',
                'subject': 'go.mod', 'detail': {'ok': True, 'host': '10.0.0.5'},
            }]
        }))
        try:
            buf = io.StringIO()
            with redirect_stdout(buf):
                scrub.cmd_deny(path, 'myuser', 'myhost')  # must not raise/exit
            self.assertIn('OK: deny-list scan clean', buf.getvalue())
        finally:
            os.remove(path)


class ManifestShape(unittest.TestCase):
    def test_manifest_lists_sorted_deduped_paths_urls_actors(self):
        text = json.dumps({
            'events': [
                {'ts': 1, 'kind': 'a', 'actor': 'builder-1', 'detail': {
                    'note': 'see https://example.com/x and /work/repo/b.go',
                }},
                {'ts': 2, 'kind': 'b', 'actor': 'builder-1', 'detail': {
                    'note': 'also /work/repo/a.go and https://example.com/x again',
                }},
                {'ts': 3, 'kind': 'c', 'actor': 'reviewer-1', 'detail': {}},
            ]
        })
        path = _write(text)
        try:
            buf = io.StringIO()
            with redirect_stdout(buf):
                scrub.cmd_manifest(path)
            out = buf.getvalue()
        finally:
            os.remove(path)

        # PATHLIKE_RE also matches the "/example.com/x" tail inside the URL's
        # "//example.com/x" — that's expected: the manifest is a human-review
        # ceiling, not a precise parser, so it over-reports rather than misses.
        self.assertIn('3 path-like string(s):', out)
        self.assertIn('/work/repo/a.go', out)
        self.assertIn('/work/repo/b.go', out)
        self.assertIn('/example.com/x', out)
        self.assertIn('1 URL(s):', out)
        self.assertIn('https://example.com/x', out)
        self.assertIn('2 unique actor name(s):', out)
        self.assertIn('builder-1', out)
        self.assertIn('reviewer-1', out)

    def test_manifest_surfaces_backslash_paths(self):
        # Windows-style paths must reach the human-review manifest even
        # though the deny gate also catches drive-lettered ones.
        text = json.dumps({'events': [{'ts': 1, 'kind': 'x', 'actor': 'builder-1',
                                        'detail': {'note': 'saved to D:\\proj\\out\\bin'}}]})
        path = _write(text)
        try:
            buf = io.StringIO()
            with redirect_stdout(buf):
                scrub.cmd_manifest(path)
            out = buf.getvalue()
        finally:
            os.remove(path)
        self.assertIn('path-like string(s):', out)
        self.assertIn('D:', out)
        self.assertIn('proj', out)
        self.assertNotIn('0 path-like string(s):', out)

    def test_manifest_no_pathlike_single_segment_names(self):
        # go.mod alone has no slash -> not path-like (requires >=2 segments)
        text = json.dumps({'events': [{'ts': 1, 'kind': 'x', 'actor': 'builder-1',
                                        'subject': 'go.mod', 'detail': {}}]})
        path = _write(text)
        try:
            buf = io.StringIO()
            with redirect_stdout(buf):
                scrub.cmd_manifest(path)
            out = buf.getvalue()
        finally:
            os.remove(path)
        self.assertIn('0 path-like string(s):', out)
        self.assertIn('0 URL(s):', out)
        self.assertIn('1 unique actor name(s):', out)


if __name__ == '__main__':
    unittest.main()

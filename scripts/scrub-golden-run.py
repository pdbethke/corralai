#!/usr/bin/env python3
# SPDX-License-Identifier: Elastic-2.0
"""scripts/scrub_golden_run.py — the golden-run export's privacy gate.

Two subcommands, both operating on the raw exported JSON text (not just the
parsed structure — a regex scan over the literal bytes catches anything
hiding in a string value, a key name, or malformed JSON alike):

  deny FILE [whoami] [hostname]
      Automated floor. Exits 1 and prints every offending line if the file
      matches an email, a /home or /Users path, a Windows drive-letter or
      backslash home path, the operator's own username/hostname, a
      token/key-shaped string, a private-key marker, an IPv4 address outside
      RFC1918/localhost, or an absolute path outside the demo containers' own
      internal roots (/work, /tmp, /root) — an escaped host path is a deny,
      not a manifest entry.

  manifest FILE
      Human-review ceiling. Prints every path-like string, every URL, and
      every unique actor/agent name found in the file, sorted and deduped —
      the operator reviews this by eye; it is not filtered by what the deny
      regexes anticipate.
"""
import ipaddress
import json
import re
import sys

DENY_PATTERNS = [
    (r'[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}', 'email address'),
    (r'/home/[A-Za-z0-9_.-]+', 'linux home-directory path'),
    (r'/Users/[A-Za-z0-9_.-]+', 'macOS home-directory path'),
    (r'gh[pousr]_[A-Za-z0-9]{20,}', 'GitHub token'),
    (r'AKIA[0-9A-Z]{16}', 'AWS access key id'),
    (r'cdt_[A-Za-z0-9]{20,}', 'vendor token (cdt_*)'),
    (r'sk-[A-Za-z0-9]{20,}', 'OpenAI-shaped API key'),
    # Anthropic keys are sk-ant-… (hyphens break the sk-[alnum]{20,} rule above),
    # and Google/Gemini keys are AIza… — the two vendors the cross-vendor critic
    # uses. Deny both outright.
    (r'sk-ant-[A-Za-z0-9_-]{20,}', 'Anthropic API key'),
    (r'\bAIza[0-9A-Za-z_-]{35}', 'Google API key'),
    (r'-----BEGIN[A-Z ]*PRIVATE KEY-----', 'private key material'),
    # Anything drive-lettered in a Linux-container demo export is suspicious
    # enough to deny outright. \\{1,2} handles both plain text (C:\Users\pat)
    # and raw JSON-escaped text (C:\\Users\\pat) alike. The (?!…) skips a lone
    # C-style escape letter as the WHOLE segment (\n \t \r \b \f \v \a \0) —
    # e.g. a Go format string `%q:\n%s` is not a `Q:\` path; a real path segment
    # (Users, network, apps) is longer or continues with path chars, so it still
    # matches.
    (r'\b[A-Za-z]:(?:\\{1,2}(?![ntrbfva0](?![A-Za-z0-9._-]))[A-Za-z0-9._-]+)+', 'Windows drive-letter path'),
    # Drive-letter-less backslash paths through home-ish segments are just as
    # host-identifying (UNC tails, mangled copies of %USERPROFILE%).
    (r'\\{1,2}(?:Users|home)\\{1,2}[A-Za-z0-9._-]+', 'Windows backslash home path'),
]

# Absolute paths that are safe because they're internal to the demo
# containers, never the operator's real host filesystem.
SAFE_PATH_PREFIXES = ('/work', '/tmp', '/root')

IPV4_RE = re.compile(r'\b(?:\d{1,3}\.){3}\d{1,3}\b')
PATHLIKE_RE = re.compile(r'(?:/[A-Za-z0-9._-]+){2,}')
# Windows-shaped strings for the manifest: an optional drive letter, then two
# or more backslash-delimited segments (\\{1,2} covers JSON-escaped text too).
WIN_PATHLIKE_RE = re.compile(r'(?:[A-Za-z]:)?(?:\\{1,2}[A-Za-z0-9._-]+){2,}')
URL_RE = re.compile(r'https?://[^\s"\']+')


def is_private_or_local(ip_str):
    try:
        ip = ipaddress.ip_address(ip_str)
    except ValueError:
        return True  # not a real IP (e.g. a version string like "1.26.4") — not our problem
    return ip.is_private or ip.is_loopback or ip.is_link_local


def scan_deny(text, whoami, hostname):
    """Return every (label, snippet) deny-list offense found in text.

    Known manifest-dependent edge: the GENERIC absolute-path rule below skips
    a leading '/' glued to a preceding word character (so Go module paths like
    github.com/yourusername/stack don't false-positive). A crafted string such
    as `word/opt/name/...` therefore bypasses that generic rule — the
    dedicated /home and /Users regexes are unaffected — and is caught only by
    the human-review manifest pass, which surfaces every path-like string
    regardless of prefix. The manifest is not optional for exactly this
    reason.
    """
    offenses = []
    for pattern, label in DENY_PATTERNS:
        for m in re.finditer(pattern, text):
            offenses.append((label, m.group(0)))
    if whoami:
        for m in re.finditer(re.escape(whoami), text):
            offenses.append(("operator's username ($(whoami))", m.group(0)))
    if hostname:
        for m in re.finditer(re.escape(hostname), text):
            offenses.append(("operator's hostname ($(hostname))", m.group(0)))
    for m in IPV4_RE.finditer(text):
        if not is_private_or_local(m.group(0)):
            offenses.append(('non-private/non-localhost IP', m.group(0)))
    for m in PATHLIKE_RE.finditer(text):
        path = m.group(0)
        # Only a path whose leading '/' actually STARTS a token is an absolute
        # filesystem path. A '/' glued to a preceding word character is the
        # tail of a domain or Go module path (github.com/yourusername/stack)
        # — not a host path, so not a deny; the manifest still surfaces it
        # for the human's eyeball pass.
        preceded_by_word = m.start() > 0 and (text[m.start() - 1].isalnum() or text[m.start() - 1] in '._-')
        if path.startswith('/') and not preceded_by_word and not path.startswith(SAFE_PATH_PREFIXES):
            offenses.append(('absolute path outside demo-container roots', path))
    return offenses


def cmd_deny(path, whoami, hostname):
    text = open(path, encoding='utf-8').read()
    offenses = scan_deny(text, whoami, hostname)
    if offenses:
        print('FAIL: golden-run export failed the deny-list scan:', file=sys.stderr)
        for label, snippet in offenses:
            print(f'  [{label}] {snippet}', file=sys.stderr)
        sys.exit(1)
    print('OK: deny-list scan clean')


def cmd_manifest(path):
    text = open(path, encoding='utf-8').read()
    paths = sorted(set(PATHLIKE_RE.findall(text)) | set(WIN_PATHLIKE_RE.findall(text)))
    urls = sorted(set(URL_RE.findall(text)))
    actors = set()
    try:
        data = json.loads(text)
        for ev in data.get('events', []):
            if ev.get('actor'):
                actors.add(ev['actor'])
    except json.JSONDecodeError:
        pass
    print('--- human-review manifest (' + path + ') ---')
    print(f'{len(paths)} path-like string(s):')
    for p in paths:
        print('  ' + p)
    print(f'{len(urls)} URL(s):')
    for u in urls:
        print('  ' + u)
    print(f'{len(actors)} unique actor name(s):')
    for a in sorted(actors):
        print('  ' + a)
    print('--- end manifest ---')


def cmd_models(path):
    """Prints a sorted JSON array of distinct model labels seen in the stream
    (backend:model when the event carries a backend in detail, bare model
    otherwise). A bare label that is the suffix of a qualified one is the SAME
    model seen from the telemetry side (no backend column there) — collapsed,
    so one model never lists twice.

    For an adversarial-pool run, the per-task telemetry backend stamp is
    unreliable for the decorrelated roles (a Gemini/OpenAI-compat critic can be
    recorded under the brain's own backend), so the authoritative model-per-role
    attribution is the signed pool_verdict's models_by_role. We union those bare
    model names in; the collapse below folds any that already appear qualified
    (e.g. bare 'claude-sonnet-5' into 'anthropic:claude-sonnet-5') while keeping
    a cross-vendor model the stream never qualified (e.g. 'gemini-3.5-flash')."""
    data = json.load(open(path, encoding='utf-8'))
    models = set()
    for ev in data.get('events', []):
        m = (ev.get('model') or '').strip()
        if not m:
            continue
        backend = ((ev.get('detail') or {}).get('backend') or '').strip()
        models.add(backend + ':' + m if backend else m)
    for ev in data.get('events', []):
        if ev.get('kind') != 'pool_verdict':
            continue
        by_role = ((ev.get('detail') or {}).get('models_by_role') or {})
        for role_model in by_role.values():
            rm = (role_model or '').strip()
            if rm:
                models.add(rm)
    models = {m for m in models if not any(o != m and o.endswith(':' + m) for o in models)}
    print(json.dumps(sorted(models)))


if __name__ == '__main__':
    if len(sys.argv) < 3:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    cmd = sys.argv[1]
    if cmd == 'deny':
        who = sys.argv[3] if len(sys.argv) > 3 else ''
        host = sys.argv[4] if len(sys.argv) > 4 else ''
        cmd_deny(sys.argv[2], who, host)
    elif cmd == 'manifest':
        cmd_manifest(sys.argv[2])
    elif cmd == 'models':
        cmd_models(sys.argv[2])
    else:
        print(__doc__, file=sys.stderr)
        sys.exit(2)

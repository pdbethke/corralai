// SPDX-License-Identifier: Elastic-2.0

package recordview

// writeJS drives the adjudication controls in corral ui --write. Ported
// from the retired uiweb page with its token rules intact: the launch
// token arrives in the URL FRAGMENT (never sent to a server), is moved to
// sessionStorage (gone with the tab, never the persistent store), is
// stripped from the address bar, and leaves only as an Authorization
// header to this origin's own api/ routes, which run corral's own
// subcommands.
//
// Differences from the uiweb page, deliberately: there is no in-page queue
// re-render (a recorded verdict says "reload the page to refresh", because
// pages are rendered from the ledger on each request); so there is also no
// ledger-read step after api/whoami, and the uiweb distinction "a ledger
// read failure is not a write-mode failure" has nothing to apply to here;
// the "Confirmed - open" list is not a separate section (confirmed
// findings show on their review page); and the recheck result is shown in
// the control's own output area. Kept from uiweb: api/whoami on load
// (bad-token detection up front), the lastwrite echo, and suggest(), which
// pre-fills the reason from verifier, recheck and citation results.
const writeJS = `<script>
(() => {
  const m = /[#&]t=([0-9a-f]{64})/.exec(location.hash);
  if (m) {
    sessionStorage.setItem('corral-ui-token', m[1]);
    history.replaceState(null, '', location.pathname + location.search);
  }
  const TOKEN = sessionStorage.getItem('corral-ui-token') || '';
  const api = p => new URL('/' + p, location.origin);
  const esc = s => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const json = async r => { const d = await r.json().catch(() => ({})); if (!r.ok) throw new Error(d.error || r.statusText); return d; };
  const write = (p, body) => fetch(api(p), {
    method: 'POST',
    headers: {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + TOKEN},
    body: JSON.stringify(body),
  }).then(json);
  const bar = document.createElement('div');
  bar.style.cssText = 'padding:.6rem 1rem;border-bottom:1px solid #8884;font-size:.88rem';
  bar.hidden = true;
  const last = document.createElement('div');
  last.id = 'lastwrite'; last.className = 'small'; last.hidden = true;
  document.body.prepend(last); document.body.prepend(bar);
  if (TOKEN) write('api/whoami', {}).then(me => {
    bar.innerHTML = 'WRITE mode - verdicts are recorded as <b>' + esc(me.by) + '</b>; rechecks run against <code>' + esc(me.repo) + '</code>.';
    bar.hidden = false;
  }, e => {
    bar.innerHTML = 'Write mode unavailable: ' + esc(e.message) + '. Open the URL <code>corral ui --write</code> printed.';
    bar.hidden = false;
  });
  const suggest = box => {
    const parts = [];
    if (box.dataset.verifier) parts.push('verifier ' + box.dataset.verifier);
    if (box.dataset.recheck) parts.push(box.dataset.recheck);
    if (box.dataset.cites) parts.push(box.dataset.cites + ' (cited by a commit message, not verified)');
    const ta = box.querySelector('textarea');
    if (!ta.value.trim() && parts.length) ta.value = parts.join('; ');
  };
  document.querySelectorAll('.controls').forEach(box => {
    const ref = box.dataset.ref, out = box.querySelector('.out');
    const verdictBtns = box.querySelectorAll('[data-verdict]');
    const ci = box.querySelector('[data-cited]');
    if (!TOKEN) {
      out.textContent = 'No launch token in this tab: open the URL corral ui --write printed. Nothing here can write.';
      box.querySelectorAll('button').forEach(b => { b.disabled = true; });
      return;
    }
    if (ci) fetch(api('api/cited?hash=' + encodeURIComponent(ref.split('#')[0])))
      .then(json)
      .then(cs => { if (Array.isArray(cs) && cs.length) { const txt = cs.map(c => c.commit + ' "' + c.subject + '"').join(', '); ci.innerHTML = 'named in: ' + esc(txt) + ' - <i>cited by a commit message, not verified</i>'; box.dataset.cites = 'named in ' + txt; suggest(box); } })
      .catch(e => { ci.textContent = 'could not check citations: ' + e.message; });
    verdictBtns.forEach(btn => btn.addEventListener('click', () => {
      const reason = box.querySelector('textarea').value.trim();
      if (!reason) { out.textContent = 'A reason is required.'; return; }
      // Both verdict buttons go dead the moment a click is accepted, so a
      // double-click or Confirm-then-Refute cannot send two adjudications.
      verdictBtns.forEach(b => { b.disabled = true; });
      out.textContent = 'running corral review adjudicate...';
      write('api/adjudicate', {ref, verdict: btn.dataset.verdict, reason})
        .then(res => { last.textContent = ref + ': ' + res.output; last.hidden = false; out.textContent = 'verdict recorded; reload the page to refresh'; },
          e => { out.textContent = 'not recorded: ' + e.message; verdictBtns.forEach(b => { b.disabled = false; }); });
    }));
    const re = box.querySelector('[data-recheck]');
    if (re) re.addEventListener('click', () => {
      re.disabled = true; out.textContent = 'running the script against HEAD...';
      write('api/recheck', {ref}).then(d => {
        const code = d.exit_code === undefined ? '' : ' (exit ' + d.exit_code + ')';
        box.dataset.recheck = 'recheck at ' + String(d.commit).slice(0, 12) + ': ' + d.outcome + code; suggest(box);
        out.innerHTML = '<b>' + esc(d.outcome) + '</b> at <code>' + esc(String(d.commit).slice(0, 12)) + '</code>' + esc(code)
          + (d.reason ? ' - ' + esc(d.reason) : '') + (d.output ? '<pre>' + esc(d.output) + '</pre>' : '');
      }).catch(e => { out.textContent = 'recheck could not run: ' + e.message; })
        .finally(() => { re.disabled = false; });
    });
  });
})();
</script>`

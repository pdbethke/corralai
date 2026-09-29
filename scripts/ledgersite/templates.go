// SPDX-License-Identifier: Elastic-2.0

package main

const baseCSS = `
:root{--bg:#fbfaf7;--fg:#1a1a18;--muted:#5f5e58;--line:#e2ded4;--card:#fff;
--accent:#2f6f4e;--warn:#9a6a00;--bad:#a33;--ok:#2f6f4e;--code:#f4f2ec;--chip:#efece3}
@media (prefers-color-scheme:dark){:root:not([data-theme=light]){--bg:#14140f;--fg:#ece9e0;
--muted:#9c988c;--line:#2e2c24;--card:#1b1a15;--accent:#7bbd94;--warn:#d9a441;--bad:#e08b8b;
--ok:#7bbd94;--code:#201f19;--chip:#262419}}
:root[data-theme=dark]{--bg:#14140f;--fg:#ece9e0;--muted:#9c988c;--line:#2e2c24;--card:#1b1a15;
--accent:#7bbd94;--warn:#d9a441;--bad:#e08b8b;--ok:#7bbd94;--code:#201f19;--chip:#262419}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);
font:16px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
.wrap{max-width:1040px;margin:0 auto;padding:40px 16px 80px}
h1{font-size:1.9rem;line-height:1.2;margin:0 0 .3em}
h2{font-size:1.25rem;margin:2.2em 0 .6em;padding-bottom:.3em;border-bottom:1px solid var(--line)}
h3{font-size:1rem;margin:1.6em 0 .4em}
a{color:var(--accent)}
code,pre,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
pre{background:var(--code);border:1px solid var(--line);border-radius:6px;padding:12px;
overflow-x:auto;font-size:.82rem;line-height:1.5;margin:.5em 0}
.lede{color:var(--muted);font-size:1.05rem;max-width:68ch}
.stats{display:flex;flex-wrap:wrap;gap:10px;margin:28px 0}
.stat{background:var(--card);border:1px solid var(--line);border-radius:8px;
padding:12px 16px;min-width:120px;flex:1 1 120px}
.stat b{display:block;font-size:1.6rem;line-height:1.1;font-variant-numeric:tabular-nums}
.stat span{color:var(--muted);font-size:.78rem;text-transform:uppercase;letter-spacing:.04em}
.banner{border:1px solid var(--line);border-left:3px solid var(--accent);background:var(--card);
border-radius:6px;padding:14px 16px;margin:20px 0}
.banner.warn{border-left-color:var(--warn)}
.banner.bad{border-left-color:var(--bad)}
.banner p{margin:.3em 0}
table{width:100%;border-collapse:collapse;font-size:.88rem;margin:1em 0}
th,td{text-align:left;padding:8px 10px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);font-size:.76rem;text-transform:uppercase;letter-spacing:.04em;font-weight:600}
tbody tr:hover{background:var(--card)}
.chip{display:inline-block;background:var(--chip);border:1px solid var(--line);border-radius:999px;
padding:1px 9px;font-size:.74rem;white-space:nowrap}
.chip.ok{color:var(--ok)} .chip.bad{color:var(--bad)} .chip.warn{color:var(--warn)}
.chip.mute{color:var(--muted)}
.meta{display:grid;grid-template-columns:max-content 1fr;gap:6px 18px;font-size:.9rem;margin:1em 0}
.meta dt{color:var(--muted)} .meta dd{margin:0;word-break:break-word}
.finding{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:16px;margin:16px 0}
.finding h3{margin-top:0}
.claim{font-size:1rem;margin:.2em 0 .6em}
.small{font-size:.82rem;color:var(--muted)}
.nowrap{white-space:nowrap}
footer{margin-top:60px;padding-top:20px;border-top:1px solid var(--line);color:var(--muted);font-size:.85rem}
details{margin:.5em 0}
summary{cursor:pointer;color:var(--accent);padding:3px 0}
summary:hover{text-decoration:underline}
ul.tight{margin:.4em 0;padding-left:1.2em} ul.tight li{margin:.15em 0}
@media (max-width:640px){.meta{grid-template-columns:1fr}.wrap{padding:24px 16px 60px}
table{font-size:.8rem}th,td{padding:6px}}
`

// verifyBanner is shared: it must say NOT CHECKED when no key was supplied,
// which is neither a pass nor a failure. `corral verify` sets that standard.
const verifyBanner = `
{{define "verify"}}
<div class="banner{{if .ChainProblems}} bad{{else if not .SigsChecked}} warn{{end}}">
  <p><strong>Chain:</strong>
  {{if .ChainProblems}}<span class="chip bad">{{.ChainProblems}} problem(s)</span>
  {{else}}<span class="chip ok">intact</span> — every entry's hash matches its bytes and every link names its predecessor, across all {{.LedgerEntries}} entries.{{end}}</p>
  <p><strong>Signatures:</strong>
  {{if .SigsChecked}}<span class="chip ok">{{.SigsOK}}/{{.SigsTotal}} verified</span>
  {{else}}<span class="chip warn">not checked</span> — this page was generated without a public key, so signature validity is <em>unknown</em>. That is not a pass and not a failure. {{.SigsTotal}} of {{.LedgerEntries}} entries carry a signature; verify them yourself with the command below.{{end}}</p>
</div>
{{end}}
`

const indexTmpl = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>corral/ledger — the record</title>
<meta name="description" content="Every audit corral ran on its own code: signed, hash-linked, and reproducible.">
<style>` + baseCSS + `</style></head><body><div class="wrap">

<h1>corral/ledger — the record</h1>
<p class="lede">Every review corral has run on <em>its own source</em>, as a signed, hash-linked
chain. Each finding carries the script that was executed to test it, and the exit code that came
back. Nothing here asks to be believed: re-run any of it.</p>

<div class="stats">
  <div class="stat"><b>{{.LedgerEntries}}</b><span>ledger entries</span></div>
  <div class="stat"><b>{{.Reviews | len}}</b><span>reviews</span></div>
  <div class="stat"><b>{{.Findings}}</b><span>findings</span></div>
  <div class="stat"><b>{{.Reproduced}}</b><span>reproduced by execution</span></div>
  <div class="stat"><b>{{.Confirmed}}</b><span>confirmed by a person</span></div>
  <div class="stat"><b>{{.Refuted}}</b><span>refuted by a person</span></div>
  <div class="stat"><b>{{.CrossVendor}}</b><span>verified by another vendor</span></div>
</div>

{{template "verify" .}}

<h2>What "reproduced" means here</h2>
<p>A finding is <span class="chip ok">reproduced</span> only when a script the reviewer wrote was
<em>executed</em> and exited 0 — the contract being: exit 0 if and only if the defect is
demonstrated. The other outcomes are kept separate on purpose, because collapsing them is how a
could-not-measure turns into a measured zero:</p>
<ul class="tight">
  <li><span class="chip ok">reproduced</span> ran, exit 0 — the defect was demonstrated.</li>
  <li><span class="chip mute">not shown</span> ran, non-zero — the claim did not demonstrate.</li>
  <li><span class="chip warn">never ran</span> the harness could not run it (worktree gone, timeout,
      missing tool). This grades nobody: it is an infrastructure failure, not a wrong claim.</li>
  <li><span class="chip mute">no script</span> nothing executable was recorded.</li>
</ul>
<p class="small">A person's verdict is a separate axis from execution, and it wins: a human
<strong>confirmed</strong>/<strong>refuted</strong> is recorded as its own signed entry and is never
clawed back by a later automatic pass.</p>

<h2>Decorrelation, {{.CrossVendor}} times over</h2>
<p>A reviewer seat finds; a <em>different model</em> verifies. The rule is enforced, not requested —
the verifier is never the reviewer's model. What actually ran:</p>
<ul class="tight">
  <li><strong>{{.CrossModel}}</strong> of {{.Reviews | len}} reviews were verified by a different model.</li>
  <li><strong>{{.CrossVendor}}</strong> were verified by a model from a <strong>different vendor</strong>
      ({{join .Vendors}}) — so the faults and the grading came from different lineages, which is
      decorrelation's point rather than its letter.</li>
  {{if .VendorUndetermined}}<li class="small">{{.VendorUndetermined}} review(s) had a seat whose vendor
      this page could not resolve from its recorded name. They are <em>excluded</em> from the
      cross-vendor count rather than assumed into it, so that number understates rather than flatters.</li>{{end}}
</ul>
<p class="small">Seats that ran across this record:</p>
<div class="meta">
  <dt>reviewer models</dt><dd class="mono">{{join .Models}}</dd>
  {{if .VerifierOnly}}<dt>verifier models</dt><dd class="mono">{{join .VerifierOnly}}</dd>{{end}}
  {{if .Tools}}<dt>tools + versions</dt><dd class="mono">{{join .Tools}}</dd>{{end}}
  {{if .Scopes}}<dt>scopes reviewed</dt><dd class="mono">{{join .Scopes}}</dd>{{end}}
</div>

<h2>Reviews</h2>
<p class="small">Newest first. {{if .First.IsZero}}{{else}}Record spans {{date .First}} to {{date .Last}}.{{end}}</p>
<table><thead><tr>
<th>pushed</th><th>scope</th><th>reviewer</th><th>verifier</th>
<th class="nowrap">findings</th><th class="nowrap">repro</th><th class="nowrap">verdicts</th><th>commit</th>
</tr></thead><tbody>
{{range .Reviews}}<tr>
<td class="nowrap"><a href="{{entryURL .}}">{{date .Pushed}}</a></td>
<td class="mono">{{with .Review}}{{.Scope}}{{end}}</td>
<td class="mono small">{{with .Review}}{{.ReviewerModel}}{{end}}</td>
<td class="mono small">{{if .HasVerifier}}{{with .Review}}{{.VerifierModel}}{{end}}
  {{if .Decorrelated}}<span class="chip ok">≠</span>{{end}}
  {{else}}<span class="chip mute">none</span>{{end}}</td>
<td>{{.Findings | len}}</td>
<td>{{if .Reproduced}}<span class="chip ok">{{.Reproduced}}</span>{{else}}0{{end}}</td>
<td class="small">{{if .Confirmed}}<span class="chip ok">{{.Confirmed}}✓</span> {{end}}{{if .Refuted}}<span class="chip bad">{{.Refuted}}✗</span>{{end}}</td>
<td class="mono small">{{if .CommitURL}}<a href="{{.CommitURL}}">{{.ShortSHA}}</a>{{else}}{{.ShortSHA}}{{end}}</td>
</tr>{{end}}
</tbody></table>

<h2>Verify it yourself</h2>
<p>The record is a git branch, so there is nothing to trust and nothing to install to read it:</p>
<pre>git clone {{if .RepoURL}}{{.RepoURL}}.git{{else}}&lt;repo&gt;{{end}} corralai
cd corralai
git fetch origin corral/ledger
git checkout corral/ledger      # 121 gzipped JSON entries under scans/

# walk the chain: every hash against its bytes, every link against its predecessor
corral ledger verify .</pre>
<p class="small">To check the <em>signatures</em> as well, pass the ledger's published Ed25519 public
key (<code>--pub &lt;hex&gt;</code>). Hash and link integrity need no key: any reader can confirm from
the files alone that no entry was edited and none removed from the middle.</p>

<footer>
<p>Generated {{stamp .Generated}} from {{.LedgerEntries}} entries on <code>corral/ledger</code>.
This page renders the record and adds no opinion about it: the chain, the signatures, the retractions
and the verdicts are all read through <code>internal/auditpush</code>, the same code the CLI uses.</p>
</footer>
</div></body></html>
` + verifyBanner

const entryTmpl = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>review {{.Short}} — corral/ledger</title>
<style>` + baseCSS + `</style></head><body><div class="wrap">

<p class="small"><a href="../index.html">← the record</a></p>
<h1>Review <span class="mono">{{.Short}}</span></h1>
{{with .Review}}
<p class="lede">{{.Scope}} at <span class="mono">{{$.ShortSHA}}</span>, reviewed by
<span class="mono">{{.ReviewerModel}}</span>{{if $.HasVerifier}}, verified by
<span class="mono">{{.VerifierModel}}</span>{{end}}.</p>

<div class="banner{{if not $.ChainHashOK}} bad{{else if not $.Signed}} warn{{end}}">
  <p><strong>This entry:</strong>
  <span class="chip{{if $.ChainHashOK}} ok{{else}} bad{{end}}">hash {{if $.ChainHashOK}}matches its bytes{{else}}MISMATCH{{end}}</span>
  <span class="chip{{if $.ChainLinkOK}} ok{{else}} bad{{end}}">link {{if $.ChainLinkOK}}{{if $.Genesis}}genesis{{else}}names its predecessor{{end}}{{else}}BROKEN{{end}}</span>
  {{if $.Signed}}<span class="chip{{if $.SigOK}} ok{{else}} warn{{end}}">signature {{if $.SigOK}}verified{{else}}not checked (no key){{end}}</span>
  {{else}}<span class="chip warn">unsigned</span>{{end}}
  {{if $.ChainProblem}}<br><span class="chip bad">{{$.ChainProblem}}</span>{{end}}</p>
</div>

<dl class="meta">
  <dt>repo</dt><dd class="mono">{{.Repo}}</dd>
  <dt>commit</dt><dd class="mono">{{if $.CommitURL}}<a href="{{$.CommitURL}}">{{.Commit}}</a>{{else}}{{.Commit}}{{end}}</dd>
  <dt>scope</dt><dd class="mono">{{.Scope}}{{if .Lang}} · {{.Lang}}{{end}}</dd>
  <dt>reviewer</dt><dd class="mono">{{.ReviewerModel}}{{if .ReviewerTool}} <span class="small">({{.ReviewerTool}})</span>{{end}}</dd>
  <dt>verifier</dt><dd class="mono">{{if $.HasVerifier}}{{.VerifierModel}}{{if .VerifierTool}} <span class="small">({{.VerifierTool}})</span>{{end}}
    {{if $.Decorrelated}}<span class="chip ok">different model than the reviewer</span>{{end}}
    {{if $.CrossVendor}}<span class="chip ok">different vendor: {{$.ReviewerVendor}} → {{$.VerifierVendor}}</span>{{end}}
    {{else}}<span class="chip mute">no verifier seat ran</span>{{end}}</dd>
  <dt>substrate</dt><dd>{{.Substrate}}</dd>
  <dt>started</dt><dd>{{stamp .StartedAt}}</dd>
  {{if .Author}}<dt>audited party</dt><dd>{{.Author}}{{if .CoAuthors}} <span class="small">· co-authored: {{.CoAuthors}}</span>{{end}}</dd>{{end}}
  <dt>files shown</dt><dd>{{.FilesShown | len}} file(s){{if .Truncated}} <span class="chip warn">truncated</span>{{end}}</dd>
  {{if .Coverage}}<dt>coverage note</dt><dd>{{.Coverage}}</dd>{{end}}
</dl>

<h2>Findings ({{$.Findings | len}})</h2>
{{if not $.Findings}}<p class="small">This review recorded no findings.</p>{{end}}
{{range $.Findings}}
<div class="finding" id="{{.ID}}">
  <h3><span class="mono">{{.ID}}</span>
    {{if .Severity}}<span class="chip">{{.Severity}}</span>{{end}}
    {{if eq .Outcome "reproduced"}}<span class="chip ok">reproduced</span>
    {{else if eq .Outcome "not-shown"}}<span class="chip mute">not shown{{if .OutcomeNote}} ({{.OutcomeNote}}){{end}}</span>
    {{else if eq .Outcome "never-ran"}}<span class="chip warn">never ran</span>
    {{else}}<span class="chip mute">no script</span>{{end}}
    {{if eq .Verdict "confirmed"}}<span class="chip ok">confirmed by a person</span>
    {{else if eq .Verdict "refuted"}}<span class="chip bad">refuted by a person</span>{{end}}
    {{if .Demoted}}<span class="chip warn">demoted</span>{{end}}
  </h3>
  <p class="claim">{{.Claim}}</p>
  <dl class="meta">
    {{if .File}}<dt>where</dt><dd class="mono">{{.File}}{{if .Line}}:{{.Line}}{{end}}</dd>{{end}}
    {{if .Tier}}<dt>tier</dt><dd>{{.Tier}}{{if .Declared}} <span class="small">(declared {{.Declared}})</span>{{end}}</dd>{{end}}
    {{if .Demoted}}<dt>demoted</dt><dd>{{.Demoted}}</dd>{{end}}
    {{if eq .Outcome "never-ran"}}<dt>never ran</dt><dd>{{.OutcomeNote}} <span class="small">— an infrastructure failure, charged to nobody</span></dd>{{end}}
  </dl>
  {{if hasPre .Script}}<details><summary class="small">the script that was executed</summary><pre>{{.Script}}</pre></details>{{end}}
  {{if hasPre .Stdout}}<details><summary class="small">what it printed</summary><pre>{{.Stdout}}</pre></details>{{end}}
  {{with .Refutation}}
  <h3 class="small">refutation — {{.Model}} <span class="chip mute">{{.Verdict}}</span></h3>
  {{if .Argument}}<p class="small">{{.Argument}}</p>{{end}}
  {{if hasPre .Script}}<details><summary class="small">the refutation's script</summary><pre>{{.Script}}</pre></details>{{end}}
  {{end}}
  {{if .Verdict}}
  <h3 class="small">human verdict</h3>
  <p class="small"><strong>{{.Verdict}}</strong> by {{.VerdictBy}}{{if .VerdictWhy}} — {{.VerdictWhy}}{{end}}</p>
  {{end}}
  <p class="small mono">ref {{.Ref}}</p>
</div>
{{end}}

{{if .Sound}}
<h2>Looked at and could not break ({{.Sound | len}})</h2>
<p class="small">Recorded so that "found nothing here" can be told apart from "never looked here".</p>
<ul class="tight">{{range .Sound}}<li class="mono small">{{.}}</li>{{end}}</ul>
{{end}}

{{if .Opinion}}
<h2>Reviewer's opinion</h2>
<div class="banner warn"><p class="small"><strong>Prose, carried but not vouched for.</strong>
The signature covers this entry's reproductions — the scripts, their output and their exit codes.
It does not make the opinion below true.</p></div>
<p>{{.Opinion}}</p>
{{end}}
{{if .VerifierOpinion}}
<h2>Verifier's opinion</h2>
<p>{{.VerifierOpinion}}</p>
{{if .VerifierNote}}<p class="small">Note: {{.VerifierNote}}</p>{{end}}
{{end}}
{{end}}

<footer><p>Entry <span class="mono">{{.Hash}}</span> · file <span class="mono">{{.File}}</span> ·
pushed {{stamp .Pushed}} · <a href="../index.html">the whole record</a></p></footer>
</div></body></html>
`

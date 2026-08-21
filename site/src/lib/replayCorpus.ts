// SPDX-License-Identifier: Elastic-2.0
//
// The corpus behind the replay's end card: one row per recorded audit, read
// straight off each tape's signed pool_verdict beat.
//
// Built at BUILD TIME on purpose. The site is backend-free by test (no
// external requests), so the end card cannot query the recordings store while
// someone is watching — but the tapes it renders came out of that store during
// prebuild, so reducing them here says the same thing with no runtime call. A
// live product cockpit can hand the player live rows through the same global.

export type CorpusAudit = {
  slug: string;
  label: string;
  path: string;
  killRate: number;
  status: string;
};

export type ReplayCorpus = {
  slug: string;              // the recording being watched, so the card can highlight it
  audits: CorpusAudit[];
};

/**
 * The label on a bar. Named by RUN, not by file: the gallery deliberately holds
 * two recordings of the same file on different branches, and labeling by file
 * rendered both as an identical truncated "awards.py · sportspicke…" — erasing
 * the one distinction the pair exists to make. The slug already carries it.
 */
function labelFor(slug: string): string {
  return slug.replace(/-audit$/, '').replace(/-/g, ' ');
}

/** The audited file, kept for the bar's tooltip rather than its label. */
function subjectPath(tape: any): string {
  const subject = (tape?.events || []).find((e: any) => e?.kind === 'pool_subject')?.detail;
  return typeof subject?.code_path === 'string' ? subject.code_path : '';
}

/**
 * Reduce every recording's tape to its verdict row. Tapes with no pool_verdict
 * (a non-audit recording, or a run that never reached a terminal status) are
 * left out rather than shown as zero — an absent measurement is not a bad one.
 */
export function buildReplayCorpus(
  streams: Record<string, unknown>,
  currentSlug: string,
): ReplayCorpus {
  const audits: CorpusAudit[] = [];
  for (const [slug, tape] of Object.entries(streams)) {
    const verdict = ((tape as any)?.events || []).find((e: any) => e?.kind === 'pool_verdict')?.detail;
    if (!verdict) continue;
    const total = Number(verdict.mutants_total ?? 0);
    const survivors = Number(verdict.survivors ?? 0);
    const rate =
      typeof verdict.dev_kill_rate === 'number'
        ? verdict.dev_kill_rate
        : total > 0
          ? (total - survivors) / total
          : NaN;
    if (!Number.isFinite(rate)) continue;
    audits.push({
      slug,
      label: labelFor(slug),
      path: subjectPath(tape),
      killRate: rate,
      status: String(verdict.status || ''),
    });
  }
  return { slug: currentSlug, audits };
}

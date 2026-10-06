// SPDX-License-Identifier: Elastic-2.0
// Shared by the field-notes index, each note page and the field-notes rail,
// so the three cannot disagree about which notes are published, their order
// or how a date reads.
import { getCollection } from 'astro:content';

// Published notes, newest first.
export async function publishedNotes() {
  return (await getCollection('fieldNotes', ({ data }) => !data.draft)).sort(
    (a, b) => b.data.pubDate.getTime() - a.data.pubDate.getTime(),
  );
}

// A stable, locale-independent date string (no Intl locale drift across the
// build host): "8 Jul 2026".
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
export const fmtDate = (d: Date) => `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]} ${d.getUTCFullYear()}`;

// SPDX-License-Identifier: Elastic-2.0

package pricing

// Current is the table every recorded cost is computed from. Every number is
// copied from the vendor's own pricing page, read on Date; nothing is from
// memory or estimated. A model whose price the page does not state is absent,
// and Cost then returns no figure for it. Update it by re-reading the pages
// and changing Date, never by editing one number.
//
// Left out on purpose, with the reason:
//   - claude-haiku-5-5: the page prices it per REQUEST by prompt length (up
//     to 100,000 tokens $0.10 in / $0.50 out, over $0.50 / $2.50). A row
//     aggregates many calls and records no per-call prompt length, so no
//     single price is true for it.
//   - claude-haiku-4-5, gemini-3.7-flash: named in docs/corral, but neither
//     is on the pages read on Date.
//   - gemini-3.1-pro-preview, gemini-2.5-pro: priced by prompt size (200k
//     tiers) and not named by any recipe.
//
// Gemini lists no cache-WRITE price (its implicit cache has no write to
// bill), so CacheWritePerMTokMicroUSD is 0 for those entries and a call that
// reported cache-write tokens gets no figure.
var Current = Table{
	Source: "https://platform.claude.com/docs/en/about-claude/pricing; https://claude.com/pricing; https://ai.google.dev/gemini-api/docs/pricing",
	Date:   "2026-10-31",
	Prices: map[string]Price{
		// https://platform.claude.com/docs/en/about-claude/pricing, row
		// "Claude Sonnet 5.5": base input $2 / MTok, 5m cache writes
		// $2.50 / MTok, cache hits $0.10 / MTok, output $10 / MTok.
		// Corral's cache_control is the default ephemeral (5m) block.
		"claude-sonnet-5-5": {
			InputPerMTokMicroUSD: 2_000_000, CachedInputPerMTokMicroUSD: 100_000,
			CacheWritePerMTokMicroUSD: 2_500_000, OutputPerMTokMicroUSD: 10_000_000,
		},
		// Same page, row "Claude Sonnet 5": base input $2 / MTok (footnote:
		// the introductory price is now the standard price), 5m cache writes
		// $2.50 / MTok, cache hits $0.20 / MTok, output $10 / MTok.
		"claude-sonnet-5": {
			InputPerMTokMicroUSD: 2_000_000, CachedInputPerMTokMicroUSD: 200_000,
			CacheWritePerMTokMicroUSD: 2_500_000, OutputPerMTokMicroUSD: 10_000_000,
		},
		// https://ai.google.dev/gemini-api/docs/pricing, paid tier standard,
		// row gemini-3.6-flash: input $0.75, cached input $0.075 (plus
		// storage), output $3.75 per 1M tokens. The page schedules a change
		// to $1.50 / $0.15 / $7.50 on Jan 1, 2027; today's rate is entered.
		"gemini-3.6-flash": {
			InputPerMTokMicroUSD: 750_000, CachedInputPerMTokMicroUSD: 75_000,
			OutputPerMTokMicroUSD: 3_750_000,
		},
		// Same page, row gemini-3.8-flash: identical figures and the same
		// scheduled Jan 1, 2027 change.
		"gemini-3.8-flash": {
			InputPerMTokMicroUSD: 750_000, CachedInputPerMTokMicroUSD: 75_000,
			OutputPerMTokMicroUSD: 3_750_000,
		},
	},
}

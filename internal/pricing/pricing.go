// SPDX-License-Identifier: Elastic-2.0

// Package pricing turns a row's token counts into dollars from a DATED price
// table, and says on every answer which table it used or why it gave none.
//
// The dollar figure is the one number on the record that corral did not
// measure: tokens are measured, the price is a vendor's published list price
// on the day the table was read. So the figure always travels with its basis
// (Table.Source and Table.Date, or the reason there is no figure), and it is
// recorded in the declared tier.
//
// There is no fallback price. A model absent from the table, a component the
// table has no price for, a seat that reported no tokens and an agent seat
// billed outside corral's sight all return nil with a basis that says so. A
// partial sum would read as the whole cost, which is the failure the project
// exists to refuse.
package pricing

import "fmt"

// Price is one model's list price in micro-USD (millionths of a dollar) per
// million tokens, so $2.50 per million is 2_500_000. Integers on purpose: a
// float sum is not reproducible across the reader and the writer of a signed
// record. A zero component means the vendor's page states no price for it,
// NOT that it is free; Cost refuses to price a call that used it.
type Price struct {
	InputPerMTokMicroUSD       int64
	CachedInputPerMTokMicroUSD int64
	CacheWritePerMTokMicroUSD  int64
	OutputPerMTokMicroUSD      int64
}

// Table is a set of prices read from the vendors' own pages on one day.
type Table struct {
	// Source is the page or pages the prices were read from.
	Source string
	// Date is the day they were read (YYYY-MM-DD).
	Date string
	// Prices is keyed by the API model id the vendor names.
	Prices map[string]Price
}

// The bases Cost can return. The first is built from the table; the rest are
// fixed or built from the call.
const (
	BasisNotMetered = "not metered: the seat reported no tokens"
	BasisAgentSeat  = "agent seat: billing not known to corral"
	// BasisInconsistent covers counts that cannot all be true: the cached and
	// cache-written tokens are parts of the input total, so they cannot
	// exceed it. Pricing them anyway would subtract into a negative.
	// BasisSeatToolsUnreadable: whether a seat was an agent comes from the
	// recorded SeatTools; when that record does not parse nothing can be said
	// about the seat, and pricing it as an API model could bill a subscription.
	BasisSeatToolsUnreadable = "seat tools unreadable: cannot tell whether the seat was an agent"
	BasisInconsistent        = "tokens inconsistent: cached and cache-write tokens exceed the input total"
)

// BasisPriced is the basis of a figure this table produced.
func (t Table) BasisPriced() string {
	return fmt.Sprintf("price table %s, %s", t.Source, t.Date)
}

// BasisNoPrice is the basis when the table cannot price the call.
func (t Table) BasisNoPrice(model string) string {
	return fmt.Sprintf("no price for %s in the price table of %s", model, t.Date)
}

// BasisPartlyUnmetered is the basis for a row where some calls reported
// tokens and some did not. The cost of only the metered part would read as
// the whole row's cost, so there is no figure.
func BasisPartlyUnmetered(unmetered, calls int) string {
	return fmt.Sprintf("not metered: %d of %d calls reported no tokens", unmetered, calls)
}

// Cost prices one row. in is the WHOLE prompt (cached and cache-written
// tokens are parts of it, as agentbackend records them); cachedIn and
// cacheWrite are those parts, 0 when the provider reported none.
//
// Order matters: an agent seat is answered first, because its billing is not
// corral's to know whatever tokens it reported; then an unmetered seat; then
// the table. micro is nil whenever basis is not BasisPriced.
//
// Arithmetic: the four components' token x price products are summed and
// divided by 1_000_000 once, rounding half up to the nearest micro-USD. One
// rounding on the sum, not one per component, so a row is off by at most half
// a micro-dollar. Products stay far inside int64 for any real run (1e10
// tokens at $50 per million is 5e17).
func (t Table) Cost(model string, in, cachedIn, cacheWrite, out int64, metered bool, agentSeat bool) (micro *int64, basis string) {
	if agentSeat {
		return nil, BasisAgentSeat
	}
	if !metered {
		return nil, BasisNotMetered
	}
	p, ok := t.Prices[model]
	if !ok {
		return nil, t.BasisNoPrice(model)
	}
	plain := in - cachedIn - cacheWrite
	if plain < 0 || cachedIn < 0 || cacheWrite < 0 || out < 0 {
		return nil, BasisInconsistent
	}
	if (plain > 0 && p.InputPerMTokMicroUSD == 0) ||
		(cachedIn > 0 && p.CachedInputPerMTokMicroUSD == 0) ||
		(cacheWrite > 0 && p.CacheWritePerMTokMicroUSD == 0) ||
		(out > 0 && p.OutputPerMTokMicroUSD == 0) {
		return nil, t.BasisNoPrice(model)
	}
	sum := plain*p.InputPerMTokMicroUSD +
		cachedIn*p.CachedInputPerMTokMicroUSD +
		cacheWrite*p.CacheWritePerMTokMicroUSD +
		out*p.OutputPerMTokMicroUSD
	v := (sum + 500_000) / 1_000_000
	return &v, t.BasisPriced()
}

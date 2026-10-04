// SPDX-License-Identifier: Elastic-2.0

package main

import "flag"

// shadowSeatFlags is every flag that names a challenger seat. They are
// registered together, by registerShadowSeatFlags only, so no command can
// offer a challenger seat without also offering its pool — a rule kept at
// one door and missing at another has been this codebase's most frequent
// defect (shadow_flags_test.go enforces it).
type shadowSeatFlags struct {
	model, pool, writerModel, writerPool, seed *string
}

// shadowSeatHelp is each flag's usage text. Every door supplies all five,
// because what a pool flag DOES differs by door — certify --local draws per
// run from one language's record, certify --repo draws once per scan from
// every language's, and doctor draws nothing — and one shared sentence was
// false at two of the three.
type shadowSeatHelp struct {
	model, pool, writerModel, writerPool, seed string
}

func registerShadowSeatFlags(fs *flag.FlagSet, h shadowSeatHelp) *shadowSeatFlags {
	return &shadowSeatFlags{
		model:       fs.String("shadow-model", "", h.model),
		pool:        fs.String("shadow-pool", "", h.pool),
		writerModel: fs.String("shadow-writer-model", "", h.writerModel),
		writerPool:  fs.String("shadow-writer-pool", "", h.writerPool),
		seed:        fs.String("shadow-seed", "", h.seed),
	}
}

// shadowSeedReplayHelp is --shadow-seed's text at the two doors that draw.
const shadowSeedReplayHelp = "replay a recorded pool draw: the seed the record printed (0x…). Refused without --shadow-pool or --shadow-writer-pool"

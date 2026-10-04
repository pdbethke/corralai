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

func registerShadowSeatFlags(fs *flag.FlagSet, modelHelp, writerHelp string) *shadowSeatFlags {
	return &shadowSeatFlags{
		model:       fs.String("shadow-model", "", modelHelp),
		pool:        fs.String("shadow-pool", "", "a comma-separated POOL of challenger generator models; each run DRAWS one by Thompson sampling over the scorecard's record for this language, and the record says which, with every member's posterior. Every member is checked for a credential before the draw. Mutually exclusive with --shadow-model. OFF unless named; NEVER gates the verdict"),
		writerModel: fs.String("shadow-writer-model", "", writerHelp),
		writerPool:  fs.String("shadow-writer-pool", "", "a comma-separated POOL of challenger WRITER models, drawn per run exactly as --shadow-pool is. Mutually exclusive with --shadow-writer-model. OFF unless named; NEVER gates the verdict"),
		seed:        fs.String("shadow-seed", "", "replay a recorded pool draw: the seed the record printed (0x…). Refused without --shadow-pool or --shadow-writer-pool"),
	}
}

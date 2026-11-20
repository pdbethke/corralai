// SPDX-License-Identifier: Elastic-2.0

package lang_test

// The evidence-promotion capability is judged by internal/reposcan, which
// imports lang and installs the judge from its init. Linking it here puts
// that judge in this package's test binary, so the parity gate
// (TestEveryLanguageHasEveryCapabilityOrSaysWhy) judges the row by the code
// that decides it.
import _ "github.com/pdbethke/corralai/internal/reposcan"

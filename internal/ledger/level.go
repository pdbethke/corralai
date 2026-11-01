// SPDX-License-Identifier: Elastic-2.0

package ledger

// SigLevel is how much a verified signature on one ledger entry shows, said
// once so that no reader (corral verify, corral ui, the public site, the JSON
// Derive serves) words it, or ranks it, for itself. It is the empty string
// when there is nothing to rank: the entry has a Problem, or it is signed but
// its signature was not checked, and a reader must not invent a level for
// either.
type SigLevel string

const (
	// LevelUnsigned: the entry carries no signature.
	LevelUnsigned SigLevel = "unsigned"
	// LevelKeylessUnpinned: a keyless signature that verified for the named
	// repository, with no workflow pinned, so any workflow or ref of the
	// repository could have produced it.
	LevelKeylessUnpinned SigLevel = "keyless-unpinned"
	// LevelKeylessPinned: a keyless signature by the workflow and ref the
	// ledger's root pinned, the pin itself checked against a root key.
	LevelKeylessPinned SigLevel = "keyless-pinned"
	// LevelKeylessPinnedUnchecked: a keyless signature by the workflow and
	// ref the ledger's pin names, the pin not checked against a root key the
	// reader named.
	LevelKeylessPinnedUnchecked SigLevel = "keyless-pinned-unchecked"
	// LevelKey: an ed25519 signature by a key the ledger's root holds or
	// registered.
	LevelKey SigLevel = "key"
)

// levelSentences is the one table of words. A level without a row is a bug
// the test over every constant catches.
var levelSentences = map[SigLevel]string{
	LevelUnsigned:               "unsigned: nothing shows who wrote it",
	LevelKeylessUnpinned:        "signed keylessly by a GitHub Actions workflow of this repository, on the ref it names; any branch could have signed, because no workflow is pinned",
	LevelKeylessPinned:          "signed keylessly by the workflow and ref this ledger's root pinned",
	LevelKeylessPinnedUnchecked: "signed keylessly by the workflow and ref this ledger's pin names; the pin was not checked against a root key you named",
	LevelKey:                    "signed by a key this ledger's root holds or registered",
}

// LevelSentence is what a level means, in the words every reader prints. The
// empty level, and any value that is not a level, has no sentence: "".
func LevelSentence(l SigLevel) string { return levelSentences[l] }

// levelOf ranks one finished check. A Problem or an unchecked signature gets
// no level; an unsigned entry with no Problem is unsigned, even among signed
// ones (its Note says so).
func levelOf(c ChainCheck) SigLevel {
	switch {
	case c.Problem != "":
		return ""
	case !c.Signed:
		return LevelUnsigned
	case !c.SigChecked || !c.SigOK:
		return ""
	case c.Keyless:
		return LevelKeylessUnpinned
	default:
		return LevelKey
	}
}

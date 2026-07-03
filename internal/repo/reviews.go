// SPDX-License-Identifier: Elastic-2.0

package repo

// Review is the forge-agnostic representation of a pull-request review event.
// The concrete types returned by GitHub, Gitea, and GitLab are mapped here by
// their respective Provider implementations.
type Review struct {
	ID          int64
	State       string // "APPROVED" | "CHANGES_REQUESTED" | "COMMENTED" | "DISMISSED"
	Body        string
	SubmittedAt string // RFC3339; sorts lexically
	User        string // login name of the reviewer
}

// ReviewComment is the forge-agnostic representation of an inline PR comment.
type ReviewComment struct {
	Path string // file path relative to repo root
	Line int    // line number (0 if not line-anchored)
	Body string
	User string // login name of the commenter
}

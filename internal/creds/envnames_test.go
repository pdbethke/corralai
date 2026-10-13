// SPDX-License-Identifier: Elastic-2.0

package creds

import (
	"slices"
	"strings"
	"testing"
)

func TestSecretShapedEnvNames(t *testing.T) {
	env := []string{
		"ANTHROPIC_API_KEY=sk-ant-VALUE1",
		"CORRALAI_BRAIN_KEY=VALUE2", // canonical, matches no pattern
		"MISTRAL_API_KEY=VALUE3",
		"CORRALAI_GIT_TOKEN=VALUE4",
		"motherduck_token=VALUE5",
		"DJANGO_SECRET=VALUE6",
		"STRIPE_SECRET_KEY=VALUE7",
		"DB_PASSWORD=VALUE8",
		"AWS_PROFILE=VALUE9",
		"GH_TOKEN=VALUE10",
		"GITHUB_TOKEN=VALUE11",
		"PATH=/usr/bin",
		"HOME=/Users/op",
		"TOKEN=notmatched",            // no underscore prefix
		"SECRETARY=x",                 // not _SECRET
		"MY_TOKENIZER=x",              // not a _TOKEN suffix
		"OPENAI_API_KEY=",             // empty: nothing to leak
		"GEMINI_API_KEY=   ",          // blank
		"ANTHROPIC_API_KEY=duplicate", // reported once
		"=weird",
		"NOEQUALS",
	}
	got := SecretShapedEnvNames(env)
	want := []string{"ANTHROPIC_API_KEY", "AWS_PROFILE", "CORRALAI_BRAIN_KEY", "CORRALAI_GIT_TOKEN", "DB_PASSWORD",
		"DJANGO_SECRET", "GH_TOKEN", "GITHUB_TOKEN", "MISTRAL_API_KEY", "STRIPE_SECRET_KEY", "motherduck_token"}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "VALUE") {
		t.Fatalf("a value leaked into the names: %v", got)
	}
}

package reposcan

import "testing"

func baseInputs() KeyInputs {
	return KeyInputs{
		SourceDigest:      "src",
		PackageDigest:     "pkg",
		GoalDigest:        "goal",
		TestSurfaceDigest: "tests",
		EngineVersion:     "v0.2.0",
		ModelSet:          "claude,gemini",
		AuditConfig:       "mutants=10",
	}
}

func TestCacheKeyStableForIdenticalInputs(t *testing.T) {
	if baseInputs().CacheKey() != baseInputs().CacheKey() {
		t.Fatal("identical inputs produced different keys")
	}
}

// Every field must participate. A field left out of the hash is an
// under-invalidation bug: a stale verdict served as if it were current.
func TestCacheKeyChangesWhenAnyFieldChanges(t *testing.T) {
	mutators := map[string]func(*KeyInputs){
		"SourceDigest":      func(k *KeyInputs) { k.SourceDigest = "x" },
		"PackageDigest":     func(k *KeyInputs) { k.PackageDigest = "x" },
		"GoalDigest":        func(k *KeyInputs) { k.GoalDigest = "x" },
		"TestSurfaceDigest": func(k *KeyInputs) { k.TestSurfaceDigest = "x" },
		"EngineVersion":     func(k *KeyInputs) { k.EngineVersion = "x" },
		"ModelSet":          func(k *KeyInputs) { k.ModelSet = "x" },
		"AuditConfig":       func(k *KeyInputs) { k.AuditConfig = "x" },
	}
	want := baseInputs().CacheKey()
	for field, mutate := range mutators {
		got := baseInputs()
		mutate(&got)
		if got.CacheKey() == want {
			t.Errorf("changing %s did not change the cache key", field)
		}
	}
}

// Field values must not be able to bleed across boundaries — "ab"+"c" and
// "a"+"bc" are different inputs and must hash differently.
func TestCacheKeyIsUnambiguous(t *testing.T) {
	a := baseInputs()
	a.SourceDigest, a.PackageDigest = "ab", "c"
	b := baseInputs()
	b.SourceDigest, b.PackageDigest = "a", "bc"
	if a.CacheKey() == b.CacheKey() {
		t.Fatal("field boundaries are ambiguous: concatenation collision")
	}
}

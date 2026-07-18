package lang

import "testing"

func TestRegistryByNameAndDetect(t *testing.T) {
	p, ok := ByName("go")
	if !ok {
		t.Fatal("go plugin not registered")
	}
	if p.Name() != "go" {
		t.Fatalf("Name() = %q, want go", p.Name())
	}
	d, ok := Detect("internal/auth/login.go")
	if !ok || d.Name() != "go" {
		t.Fatalf("Detect(.go) = %v,%v; want go,true", d, ok)
	}
	if _, ok := ByName("cobol"); ok {
		t.Fatal("ByName(cobol) must be false — fail closed")
	}
	if _, ok := Detect("x.cobol"); ok {
		t.Fatal("Detect(.cobol) must be false — fail closed")
	}
}

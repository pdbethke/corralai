package corralfixture

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add")
	}
	if Sign(4) != 1 {
		t.Fatal("Sign")
	}
	Empty()
}

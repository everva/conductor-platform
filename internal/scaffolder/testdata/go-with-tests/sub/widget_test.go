package sub

import "testing"

func TestWidget(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math is broken")
	}
}

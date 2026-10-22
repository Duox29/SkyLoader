package versioncmp

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.2", "1.10", -1},
		{"2.0.0-beta", "2.0.0-alpha", 1},
		{"1.4.0", "1.4", 0},
		{"", "0.0.1", -1},
	}
	for _, test := range tests {
		if got := Compare(test.a, test.b); got != test.want {
			t.Fatalf("Compare(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}

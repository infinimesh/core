package log

import "testing"

func TestSingleLine(t *testing.T) {
	got := SingleLine("a\nb\rc")
	if got != "abc" {
		t.Fatalf("SingleLine = %q", got)
	}
}

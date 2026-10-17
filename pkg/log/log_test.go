package log

import "testing"

func TestNewLogger(t *testing.T) {
	l := NewLogger()
	if l == nil {
		t.Fatal("nil logger")
	}
	l.Info("coverage")
	_ = l.Sync()
}

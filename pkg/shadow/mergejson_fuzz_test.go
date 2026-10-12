package shadow

import (
	"encoding/json"
	"testing"
)

func FuzzMergeJSON(f *testing.F) {
	f.Add([]byte(`{"a":1}`), []byte(`{"b":2}`))
	f.Add([]byte(""), []byte(`{"b":2}`))
	f.Add([]byte(`{"a":1}`), []byte(""))
	f.Fuzz(func(t *testing.T, old, newer []byte) {
		out, err := MergeJSON(old, newer)
		if err != nil || len(out) == 0 {
			return
		}
		if json.Valid(old) && json.Valid(newer) && !json.Valid(out) {
			t.Fatalf("MergeJSON returned invalid JSON: %q", out)
		}
	})
}

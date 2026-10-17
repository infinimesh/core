package convert

import (
	"bytes"
	"testing"
)

func TestConvertBytes(t *testing.T) {
	in := []byte("name: widget\nmeta:\n  a: 1\n  enabled: true\ntags:\n  - x\n  - label: z\n")
	out, err := ConvertBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":"widget"`, `"a":1`, `"enabled":true`, `"tags"`, `"label"`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}

	out, err = ConvertBytes([]byte("- a: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"a"`)) {
		t.Fatalf("%s", out)
	}

	out, err = ConvertBytes([]byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `"hello"` {
		t.Fatalf("%s", out)
	}

	if _, err = ConvertBytes([]byte(":\n  - [")); err == nil {
		t.Fatal("expected invalid yaml")
	}
}

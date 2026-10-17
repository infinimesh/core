package rules

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreFileAndRepeat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alarms.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(filepath.Join(dir, "missing", "alarms.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("bad json")
	}
	if err := os.WriteFile(path, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	asDir := filepath.Join(dir, "adir")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(asDir); err == nil {
		t.Fatal("dir read")
	}

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rule := &Rule{Name: "hot", Repeat: duration(time.Minute)}
	ev, err := store.Step(rule, "dev", true, true, false, true, now)
	if err != nil || ev == nil || ev.Kind != "raised" {
		t.Fatalf("raise %v %v", ev, err)
	}
	ev, err = store.Step(rule, "dev", true, true, false, false, now.Add(2*time.Minute))
	if err != nil || ev == nil || ev.Kind != "repeat" {
		t.Fatalf("repeat %v %v", ev, err)
	}
	loaded, err := OpenStore(path)
	if err != nil || len(loaded.List()) != 1 {
		t.Fatalf("reload %+v %v", loaded.List(), err)
	}
	if _, err := store.Ack("missing", "dev", now); err == nil {
		t.Fatal("ack missing")
	}
	store.Put(nil)
	store.Put(&Alarm{Rule: "other", Device: "dev", State: Raised})
	store.Replace([]*Alarm{{Rule: "", Device: "dev"}, nil, {Rule: "kept", Device: "dev", State: Cleared}})
	if got := store.List(); len(got) != 1 || got[0].Rule != "kept" {
		t.Fatalf("%+v", got)
	}

	missingDir := filepath.Join(dir, "nope", "alarms.json")
	broken := &Store{path: missingDir, alarms: map[string]*Alarm{"a/b": {Rule: "a", Device: "b"}}}
	if err := broken.saveLocked(); err == nil {
		t.Fatal("create temp")
	}
	dest := filepath.Join(dir, "dest")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	renamed := &Store{path: dest, alarms: map[string]*Alarm{"a/b": {Rule: "a", Device: "b"}}}
	if err := renamed.saveLocked(); err == nil {
		t.Fatal("rename")
	}
	if err := (&Store{alarms: map[string]*Alarm{}}).saveLocked(); err != nil {
		t.Fatal(err)
	}
}

package rules

import (
	"testing"
	"time"
)

func TestSameNameRulesDoNotShareAnAlarm(t *testing.T) {
	eng, err := NewEngine([]*Rule{
		{ID: "id-a", Name: "hot", When: "true"},
		{ID: "id-b", Name: "hot", When: "true"},
	}, nil, &Actions{sleep: func(time.Duration) {}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10, 0).UTC()
	eng.SetNow(func() time.Time { return now })
	eng.Check(Vars{Device: "d1", Title: "pump-7"}, nil)
	list := eng.store.List()
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
	got := map[string]Alarm{}
	for _, a := range list {
		if a.Title != "hot" || a.State != Raised || a.Count != 1 {
			t.Fatalf("%+v", a)
		}
		got[a.Rule] = *a
	}
	if _, ok := got["id-a"]; !ok {
		t.Fatalf("%+v", list)
	}
	if _, ok := got["id-b"]; !ok {
		t.Fatalf("%+v", list)
	}
	if _, err := eng.store.Ack("id-a", "d1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, a := range eng.store.List() {
		if a.Rule == "id-b" && a.State != Raised {
			t.Fatalf("acking id-a changed %s to %s", a.Rule, a.State)
		}
	}
	if _, err := NewEngine([]*Rule{{Name: "hot", When: "true"}, {Name: "hot", When: "true"}}, nil, &Actions{sleep: func(time.Duration) {}}); err == nil {
		t.Fatal("rules without a uuid still share a name")
	}
}

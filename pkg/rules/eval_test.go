package rules

import (
	"testing"

	"github.com/infinimesh/proto/shadow"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestApplyRaisesWhenConnected(t *testing.T) {
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine([]*Rule{{
		Name: "up", When: "connected", OnClear: true, Actions: []Action{{Log: &LogAction{}}},
	}}, store, NewActions())
	if err != nil {
		t.Fatal(err)
	}
	Apply(eng, &shadow.Shadow{Device: "dev", Namespace: "plant", Connection: &shadow.ConnectionState{Connected: true}})
	list := store.List()
	if len(list) != 1 || list[0].State != Raised || list[0].Device != "dev" {
		t.Fatalf("%#v", list)
	}
	reported, _ := structpb.NewStruct(map[string]any{"t": "hot"})
	eng2, err := NewEngine([]*Rule{{
		Name: "hot", When: `reported.t == "hot"`, Actions: []Action{{Log: &LogAction{}}},
	}}, store, NewActions())
	if err != nil {
		t.Fatal(err)
	}
	Apply(eng2, &shadow.Shadow{Device: "dev", Reported: &shadow.State{Data: reported}})
	found := false
	for _, a := range store.List() {
		if a.Rule == "hot" && a.State == Raised {
			found = true
		}
	}
	if !found {
		t.Fatal(store.List())
	}
}

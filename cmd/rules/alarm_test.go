package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/infinimesh/core/pkg/graph"
	"github.com/infinimesh/core/pkg/rules"
)

func TestAlarmDocumentKeepsAck(t *testing.T) {
	in := &rules.Alarm{Rule: "id-1", Device: "dev", Title: "hot", State: rules.Acked, Count: 2, AckedAt: time.Unix(30, 0).UTC()}
	raw, err := json.Marshal(graph.AlarmDocument(in, "plant"))
	if err != nil {
		t.Fatal(err)
	}
	var back graph.StoredAlarm
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Alarm()
	if got.State != rules.Acked || got.Count != 2 || got.Title != "hot" || !got.AckedAt.Equal(in.AckedAt) {
		t.Fatalf("%s -> %+v", raw, got)
	}
	if back.Namespace != "plant" || back.Key != "id-1_dev" {
		t.Fatalf("%+v", back)
	}
}

package main

import (
	"testing"
	"time"

	"github.com/infinimesh/core/pkg/rules"
	rpb "github.com/infinimesh/proto/node/rules"
)

func TestAlarmRoundTripKeepsAck(t *testing.T) {
	in := &rules.Alarm{Rule: "hot", Device: "dev", State: rules.Acked, Count: 2, AckedAt: time.Unix(30, 0).UTC()}
	back := fromProtoAlarm(toProtoAlarm(in, "plant"))
	if back.State != rules.Acked || back.Count != 2 || back.AckedAt.Unix() != 30 {
		t.Fatalf("%#v", back)
	}
	if toProtoAlarm(in, "plant").GetState() != rpb.AlarmState_ACKED {
		t.Fatal("state")
	}
}

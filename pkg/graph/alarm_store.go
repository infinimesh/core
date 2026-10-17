package graph

import (
	"encoding/json"
	"time"

	"github.com/infinimesh/core/v3/pkg/rules"
	rpb "github.com/infinimesh/proto/node/rules"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// StoredAlarm is the ArangoDB document for one rule and one device.
// The driver decodes a document by calling encoding/json on every field whose
// name matches a key. A protobuf Alarm has an unexported field named state, and
// the alarm's own state key selects that field once it is non-zero.
type StoredAlarm struct {
	Key       string     `json:"_key,omitempty"`
	Rule      string     `json:"rule"`
	Device    string     `json:"device"`
	Title     string     `json:"title,omitempty"`
	State     alarmState `json:"state"`
	RaisedAt  storedTime `json:"raised_at,omitempty"`
	FiredAt   storedTime `json:"fired_at,omitempty"`
	AckedAt   storedTime `json:"acked_at,omitempty"`
	ClearedAt storedTime `json:"cleared_at,omitempty"`
	Count     int        `json:"count"`
	Namespace string     `json:"namespace,omitempty"`
}

func AlarmKey(rule, device string) string { return rule + "_" + device }

func AlarmDocument(a *rules.Alarm, ns string) StoredAlarm {
	if a == nil {
		return StoredAlarm{}
	}
	return StoredAlarm{
		Key: AlarmKey(a.Rule, a.Device), Rule: a.Rule, Device: a.Device, Title: a.Title,
		State: alarmState(a.State), RaisedAt: storedTime(a.RaisedAt), FiredAt: storedTime(a.FiredAt),
		AckedAt: storedTime(a.AckedAt), ClearedAt: storedTime(a.ClearedAt), Count: a.Count, Namespace: ns,
	}
}

func (d StoredAlarm) Alarm() *rules.Alarm {
	return &rules.Alarm{
		Rule: d.Rule, Device: d.Device, Title: d.Title, State: string(d.State), Count: d.Count,
		RaisedAt: time.Time(d.RaisedAt), FiredAt: time.Time(d.FiredAt),
		AckedAt: time.Time(d.AckedAt), ClearedAt: time.Time(d.ClearedAt),
	}
}

func (d StoredAlarm) Proto() *rpb.Alarm {
	st := rpb.AlarmState_RAISED
	switch string(d.State) {
	case rules.Acked:
		st = rpb.AlarmState_ACKED
	case rules.Cleared:
		st = rpb.AlarmState_CLEARED
	}
	out := &rpb.Alarm{
		Rule: d.Rule, Device: d.Device, Title: d.Title, State: st, Count: int32(d.Count), Namespace: d.Namespace,
	}
	stamp := func(t storedTime) *timestamppb.Timestamp {
		if time.Time(t).IsZero() {
			return nil
		}
		return timestamppb.New(time.Time(t))
	}
	out.RaisedAt = stamp(d.RaisedAt)
	out.FiredAt = stamp(d.FiredAt)
	out.AckedAt = stamp(d.AckedAt)
	out.ClearedAt = stamp(d.ClearedAt)
	return out
}

type alarmState string

func (s alarmState) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

func (s *alarmState) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = alarmState(v)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	switch n {
	case int(rpb.AlarmState_ACKED):
		*s = alarmState(rules.Acked)
	case int(rpb.AlarmState_CLEARED):
		*s = alarmState(rules.Cleared)
	default:
		*s = alarmState(rules.Raised)
	}
	return nil
}

type storedTime time.Time

func (t storedTime) MarshalJSON() ([]byte, error) {
	if time.Time(t).IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(time.Time(t).UTC().Format(time.RFC3339Nano))
}

func (t *storedTime) UnmarshalJSON(b []byte) error {
	if string(b) == "null" || string(b) == `""` {
		*t = storedTime{}
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*t = storedTime{}
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return err
		}
		*t = storedTime(parsed)
		return nil
	}
	var stamp struct {
		Seconds int64 `json:"seconds"`
		Nanos   int32 `json:"nanos"`
	}
	if err := json.Unmarshal(b, &stamp); err != nil {
		return err
	}
	if stamp.Seconds == 0 && stamp.Nanos == 0 {
		*t = storedTime{}
		return nil
	}
	*t = storedTime(time.Unix(stamp.Seconds, int64(stamp.Nanos)).UTC())
	return nil
}

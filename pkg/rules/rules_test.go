package rules

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestStepCooldownAndAck(t *testing.T) {
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	rule := &Rule{Name: "hot", Cooldown: duration(10 * time.Minute)}
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	ev, err := store.Step(rule, "dev", true, true, false, true, now)
	if err != nil || ev == nil || ev.Kind != "raised" {
		t.Fatalf("raise: %v %v", ev, err)
	}
	ev, err = store.Step(rule, "dev", false, true, true, true, now.Add(time.Minute))
	if err != nil || ev == nil || ev.Kind != "cleared" {
		t.Fatalf("clear: %v %v", ev, err)
	}
	ev, err = store.Step(rule, "dev", true, true, false, true, now.Add(2*time.Minute))
	if err != nil || ev != nil {
		t.Fatalf("cooldown should hold, got %v %v", ev, err)
	}
	ev, err = store.Step(rule, "dev", true, true, false, true, now.Add(11*time.Minute))
	if err != nil || ev == nil || ev.Kind != "raised" || ev.Alarm.Count != 2 {
		t.Fatalf("second raise: %v %v", ev, err)
	}
	if _, err := store.Ack("hot", "dev", now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ev, err = store.Step(rule, "dev", true, true, false, true, now.Add(time.Hour))
	if err != nil || ev != nil {
		t.Fatalf("acked alarm must not repeat, got %v %v", ev, err)
	}
}

func TestClearedAlarmReturnsWhileConditionHolds(t *testing.T) {
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	rule := &Rule{Name: "hot"}
	now := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	ev, err := store.Step(rule, "dev", true, true, false, true, now)
	if err != nil || ev == nil || ev.Kind != "raised" || ev.Alarm.Title != "hot" {
		t.Fatalf("raise: %v %v", ev, err)
	}
	ev, err = store.Step(rule, "dev", true, true, true, true, now.Add(time.Second))
	if err != nil || ev == nil || ev.Kind != "cleared" {
		t.Fatalf("clear: %v %v", ev, err)
	}
	ev, err = store.Step(rule, "dev", true, true, false, true, now.Add(2*time.Second))
	if err != nil || ev == nil || ev.Kind != "raised" || ev.Alarm.Count != 2 || ev.Alarm.Title != "hot" {
		t.Fatalf("raise again: %v %v", ev, err)
	}
}

func TestCELAndWebhook(t *testing.T) {
	var mu sync.Mutex
	var got Payload
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Infinimesh-Signature") != sign("s3cret", b) {
			t.Errorf("signature %s", r.Header.Get("X-Infinimesh-Signature"))
		}
		mu.Lock()
		_ = json.Unmarshal(b, &got)
		mu.Unlock()
	}))
	defer hook.Close()
	file, err := Parse([]byte(`
rules:
  - name: hot
    when: reported.temp > 80 && "pump" in tags
    clear: reported.temp < 75
    on_clear: true
    actions:
      - webhook: {url: "`+hook.URL+`", secret: "s3cret"}
      - desired: {fan: "on"}
`), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	desired := &fakeDesired{}
	eng, err := NewEngine(file.Rules, nil, &Actions{
		client: hook.Client(), retries: 0, backoff: time.Millisecond, maxBackoff: time.Millisecond,
		sleep: func(time.Duration) {}, desired: desired,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.Check(Vars{Device: "d1", Title: "pump-7", Tags: []string{"pump"}, Namespace: "plant", Reported: map[string]any{"temp": 85.0}}, nil)
	mu.Lock()
	if got.Event != "raised" || got.Rule != "hot" || got.Device != "d1" {
		t.Fatalf("payload %+v", got)
	}
	mu.Unlock()
	if len(desired.calls) != 1 || desired.calls[0]["fan"] != "on" {
		t.Fatalf("desired %+v", desired.calls)
	}
	eng.Check(Vars{Device: "d1", Title: "pump-7", Tags: []string{"pump"}, Namespace: "plant", Reported: map[string]any{"temp": 70.0}}, nil)
	if len(desired.calls) != 1 {
		t.Fatalf("desired must not run on clear, calls %d", len(desired.calls))
	}
}

func TestMissingFieldLeavesAlarm(t *testing.T) {
	file, err := Parse([]byte("rules: [{name: hot, when: 'reported.temp > 80'}]"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(file.Rules, nil, &Actions{sleep: func(time.Duration) {}})
	if err != nil {
		t.Fatal(err)
	}
	eng.Check(Vars{Device: "d1", Reported: map[string]any{}}, nil)
	if len(eng.store.List()) != 0 {
		t.Fatalf("missing field changed the alarm: %+v", eng.store.List())
	}
}

type fakeDesired struct{ calls []map[string]any }

func (f *fakeDesired) PatchDesired(_ string, d map[string]any) error {
	f.calls = append(f.calls, d)
	return nil
}

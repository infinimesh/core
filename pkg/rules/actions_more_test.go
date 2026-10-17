package rules

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type recordingPub struct {
	topic string
	qos   byte
	body  []byte
	err   error
}

func (p *recordingPub) Publish(topic string, qos byte, retain bool, body []byte) error {
	p.topic, p.qos, p.body = topic, qos, body
	return p.err
}

func TestActionsMQTTDesiredAndRetry(t *testing.T) {
	var x *Actions
	x.SetMQTT(nil)
	x.SetDesired(nil)

	var hits atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := hits.Add(1)
		switch {
		case n == 1:
			w.WriteHeader(http.StatusInternalServerError)
		case n == 2:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer hook.Close()
	slept := 0
	actions := NewActions()
	actions.retries = 2
	actions.backoff = time.Millisecond
	actions.maxBackoff = 2 * time.Millisecond
	actions.sleep = func(time.Duration) { slept++ }
	actions.client = hook.Client()
	pub := &recordingPub{}
	actions.SetMQTT(pub)
	desired := &errDesired{}
	actions.SetDesired(desired)
	actions.Run(&Rule{Actions: []Action{
		{Log: &LogAction{}},
		{Log: &LogAction{Message: "hot {device} {rule} {event}"}},
		{Webhook: &WebhookAction{URL: hook.URL, Secret: "s"}},
		{MQTT: &MQTTAction{Topic: "alarm/{device}", QoS: 1, Retain: true}},
		{Desired: map[string]any{"fan": "on"}},
	}}, &Payload{Event: "raised", Rule: "hot", Device: "dev"})
	if slept < 2 || pub.topic != "alarm/dev" || pub.qos != 1 || len(desired.calls) != 1 {
		t.Fatalf("slept %d topic %s calls %d", slept, pub.topic, len(desired.calls))
	}

	pub.err = errors.New("mqtt")
	actions.mqtt = nil
	actions.Run(&Rule{Actions: []Action{{MQTT: &MQTTAction{Topic: "t"}}}}, &Payload{Rule: "hot", Device: "dev"})
	actions.mqtt = pub
	actions.Run(&Rule{Actions: []Action{{MQTT: &MQTTAction{Topic: "t"}}}}, &Payload{Rule: "hot", Device: "dev"})

	bad := &Actions{client: hook.Client(), retries: 0, backoff: time.Millisecond, maxBackoff: time.Millisecond, sleep: func(time.Duration) {}}
	if err := bad.webhook(&WebhookAction{URL: "http://[::1"}, "dev", []byte("{}")); err == nil {
		t.Fatal("bad url")
	}
	down := &Actions{
		client: hook.Client(), retries: 1, backoff: time.Millisecond, maxBackoff: time.Millisecond, sleep: func(time.Duration) {},
	}
	if err := down.webhook(&WebhookAction{URL: "http://127.0.0.1:1"}, "dev", []byte("{}")); err == nil {
		t.Fatal("dial")
	}
	once := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer once.Close()
	if err := down.webhook(&WebhookAction{URL: once.URL}, "dev", []byte("{}")); err == nil || slept < 0 {
		t.Fatal(err)
	}
	if !retryable(http.StatusTooManyRequests) || !retryable(http.StatusRequestTimeout) || !retryable(500) || retryable(404) {
		t.Fatal("retryable")
	}
	actions.Run(&Rule{Name: "hot"}, &Payload{Shadow: map[string]any{"bad": make(chan int)}})
}

type errDesired struct{ fakeDesired }

func (e *errDesired) PatchDesired(device string, data map[string]any) error {
	e.calls = append(e.calls, data)
	return errors.New("patch")
}

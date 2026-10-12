package rules

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Payload is what webhook and MQTT actions send.
type Payload struct {
	Event     string         `json:"event"`
	Rule      string         `json:"rule"`
	Device    string         `json:"device"`
	Title     string         `json:"title,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Alarm     Alarm          `json:"alarm"`
	Shadow    map[string]any `json:"shadow,omitempty"`
	SentAt    string         `json:"sent_at"`
}

type publisher interface {
	Publish(topic string, qos byte, retain bool, body []byte) error
}

type desiredPatcher interface {
	PatchDesired(device string, data map[string]any) error
}

// Actions runs the actions of a rule.
type Actions struct {
	client     *http.Client
	retries    int
	backoff    time.Duration
	maxBackoff time.Duration
	sleep      func(time.Duration)
	mqtt       publisher
	desired    desiredPatcher
}

func NewActions() *Actions {
	return &Actions{
		client:     &http.Client{Timeout: 10 * time.Second},
		retries:    5,
		backoff:    500 * time.Millisecond,
		maxBackoff: 30 * time.Second,
		sleep:      time.Sleep,
	}
}

func (x *Actions) SetMQTT(p publisher) {
	if x != nil {
		x.mqtt = p
	}
}

func (x *Actions) SetDesired(d desiredPatcher) {
	if x != nil {
		x.desired = d
	}
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func expand(s string, p *Payload) string {
	return strings.NewReplacer("{device}", p.Device, "{rule}", p.Rule, "{event}", p.Event).Replace(s)
}

// Run runs all actions and logs failures. One failed action does not stop
// the others.
func (x *Actions) Run(r *Rule, p *Payload) {
	body, err := json.Marshal(p)
	if err != nil {
		log.Printf("rule %s device %s: %s\n", p.Rule, p.Device, err)
		return
	}
	for i, a := range r.Actions {
		var err error
		switch {
		case a.Log != nil:
			msg := a.Log.Message
			if msg == "" {
				msg = "alarm"
			}
			log.Printf("rule %s device %s (%s): %s %s\n", p.Rule, p.Device, p.Title, p.Event, expand(msg, p))
		case a.Webhook != nil:
			err = x.webhook(a.Webhook, p.Device, body)
		case a.MQTT != nil:
			if x.mqtt == nil {
				err = fmt.Errorf("RULES_MQTT_BROKER is not set")
			} else {
				err = x.mqtt.Publish(expand(a.MQTT.Topic, p), a.MQTT.QoS, a.MQTT.Retain, body)
			}
		case a.Desired != nil:
			if p.Event == "cleared" {
				continue // desired state is set on raise only
			}
			err = x.desired.PatchDesired(p.Device, a.Desired)
		}
		if err != nil {
			log.Printf("rule %s device %s action %d failed: %s\n", p.Rule, p.Device, i+1, err)
		}
	}
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusRequestTimeout || status >= 500
}

func (x *Actions) webhook(w *WebhookAction, device string, body []byte) error {
	wait := x.backoff
	var last error
	for attempt := 0; attempt <= x.retries; attempt++ {
		if attempt > 0 {
			x.sleep(wait)
			if wait *= 2; wait > x.maxBackoff {
				wait = x.maxBackoff
			}
		}
		status, err := x.post(w, device, body)
		switch {
		case err != nil:
			last = err
		case status >= 200 && status < 300:
			return nil
		case retryable(status):
			last = fmt.Errorf("status %d", status)
		default:
			return fmt.Errorf("status %d, not retried", status)
		}
	}
	return fmt.Errorf("gave up after %d tries: %w", x.retries+1, last)
}

func (x *Actions) post(w *WebhookAction, device string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "infinimesh-rules")
	req.Header.Set("X-Infinimesh-Device", device)
	if w.Secret != "" {
		req.Header.Set("X-Infinimesh-Signature", sign(w.Secret, body))
	}
	res, err := x.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	res.Body.Close()
	return res.StatusCode, nil
}

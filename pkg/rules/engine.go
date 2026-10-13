package rules

import (
	"fmt"
	"time"
)

// Engine checks rules against a device shadow.
type Engine struct {
	rules   []*Rule
	store   *Store
	actions *Actions
	now     func() time.Time
}

func NewEngine(rules []*Rule, store *Store, actions *Actions) (*Engine, error) {
	env, err := newEnv()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r == nil || r.Name == "" {
			return nil, fmt.Errorf("rule name is required")
		}
		id := r.alarmKey()
		if seen[id] {
			return nil, fmt.Errorf("rule %s: name used twice", r.Name)
		}
		seen[id] = true
		if r.when, err = compile(env, r.When); err != nil {
			return nil, fmt.Errorf("rule %s: when: %w", r.Name, err)
		}
		if r.Clear != "" {
			if r.clear, err = compile(env, r.Clear); err != nil {
				return nil, fmt.Errorf("rule %s: clear: %w", r.Name, err)
			}
		}
		if len(r.Actions) == 0 {
			r.Actions = []Action{{Log: &LogAction{}}}
		}
	}
	if store == nil {
		store, err = OpenStore("")
		if err != nil {
			return nil, err
		}
	}
	if actions == nil {
		actions = &Actions{client: nil, retries: 0, backoff: time.Millisecond, maxBackoff: time.Millisecond, sleep: func(time.Duration) {}}
	}
	return &Engine{rules: rules, store: store, actions: actions, now: time.Now}, nil
}

func (e *Engine) SetNow(now func() time.Time) {
	if now != nil {
		e.now = now
	}
}

// Check evaluates every rule for one device. shadow is included in action payloads.
func (e *Engine) Check(v Vars, shadow map[string]any) {
	now := e.now()
	for _, r := range e.rules {
		when, whenOK := eval(r.when, v)
		clear, clearOK := !when, whenOK
		if r.clear != nil {
			clear, clearOK = eval(r.clear, v)
		}
		ev, err := e.store.Step(r, v.Device, when, whenOK, clear, clearOK, now)
		if err != nil || ev == nil || (ev.Kind == "cleared" && !r.OnClear) {
			continue
		}
		if e.actions == nil || e.actions.client == nil && len(r.Actions) == 0 {
			continue
		}
		e.actions.Run(r, &Payload{
			Event: ev.Kind, Rule: r.Name, Device: v.Device, Title: v.Title,
			Namespace: v.Namespace, Tags: v.Tags, Alarm: ev.Alarm, Shadow: shadow,
			SentAt: now.UTC().Format(time.RFC3339),
		})
	}
}

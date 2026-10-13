package rules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	Raised  = "raised"
	Acked   = "acked"
	Cleared = "cleared"
)

// Alarm is the state of one rule for one device.
type Alarm struct {
	Rule      string    `json:"rule"`
	Device    string    `json:"device"`
	Title     string    `json:"title,omitempty"`
	State     string    `json:"state"`
	RaisedAt  time.Time `json:"raised_at"`
	FiredAt   time.Time `json:"fired_at"`
	AckedAt   time.Time `json:"acked_at,omitzero"`
	ClearedAt time.Time `json:"cleared_at,omitzero"`
	Count     int       `json:"count"`
}

func key(rule, device string) string { return rule + "/" + device }

// Store keeps alarms in memory and writes them to a JSON file on every change.
type Store struct {
	mu     sync.Mutex
	path   string
	alarms map[string]*Alarm
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, alarms: map[string]*Alarm{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Alarm
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, a := range list {
		s.alarms[key(a.Rule, a.Device)] = a
	}
	return s, nil
}

// saveLocked writes the file through a temp file and rename, so a crash
// never leaves half a file.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.listLocked(), "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".alarms-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), s.path)
}

func (s *Store) listLocked() []*Alarm {
	list := make([]*Alarm, 0, len(s.alarms))
	for _, a := range s.alarms {
		c := *a
		list = append(list, &c)
	}
	sort.Slice(list, func(i, j int) bool { return key(list[i].Rule, list[i].Device) < key(list[j].Rule, list[j].Device) })
	return list
}

func (s *Store) List() []*Alarm {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) Replace(list []*Alarm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alarms = map[string]*Alarm{}
	for _, a := range list {
		if a == nil || a.Rule == "" || a.Device == "" {
			continue
		}
		c := *a
		s.alarms[key(c.Rule, c.Device)] = &c
	}
}

func (s *Store) Put(a *Alarm) {
	if a == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *a
	s.alarms[key(a.Rule, a.Device)] = &c
}

// Ack marks a raised alarm as acknowledged. It stops repeats.
func (s *Store) Ack(rule, device string, now time.Time) (*Alarm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.alarms[key(rule, device)]
	if a == nil || a.State != Raised {
		return nil, errNotRaised
	}
	a.State, a.AckedAt = Acked, now
	c := *a
	return &c, s.saveLocked()
}

var errNotRaised = errors.New("no raised alarm for this rule and device")

// Event tells what should be sent.
type Event struct {
	Kind  string // "raised", "repeat" or "cleared"
	Alarm Alarm
}

// Step moves the alarm of rule r for one device. when is the result of the
// when condition, clear of the clear condition (or !when without one). Both
// come with an ok flag; a condition that cannot be decided changes nothing.
// It returns the event to fire, if any. The stored alarm key is the rule uuid
// when the rule has one, and title is the rule name.
func (s *Store) Step(r *Rule, device string, when, whenOK, clear, clearOK bool, now time.Time) (*Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.alarmKey()
	k := key(id, device)
	a := s.alarms[k]
	active := a != nil && (a.State == Raised || a.State == Acked)
	var ev *Event
	switch {
	case !active && whenOK && when:
		if a != nil && r.Cooldown > 0 && now.Sub(a.FiredAt) < time.Duration(r.Cooldown) {
			return nil, nil // still cooling down from the last raise
		}
		if a == nil {
			a = &Alarm{Rule: id, Device: device}
			s.alarms[k] = a
		}
		a.Title, a.State, a.RaisedAt, a.FiredAt, a.Count = r.Name, Raised, now, now, a.Count+1
		a.AckedAt, a.ClearedAt = time.Time{}, time.Time{}
		ev = &Event{Kind: "raised", Alarm: *a}
	case active && clearOK && clear:
		a.State, a.ClearedAt = Cleared, now
		ev = &Event{Kind: "cleared", Alarm: *a}
	case active && a.State == Raised && r.Repeat > 0 && now.Sub(a.FiredAt) >= time.Duration(r.Repeat):
		a.FiredAt = now
		ev = &Event{Kind: "repeat", Alarm: *a}
	default:
		return nil, nil
	}
	return ev, s.saveLocked()
}

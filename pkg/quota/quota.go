package quota

import (
	"sync"
	"time"
)

// Limiter is a token bucket. A rate of 0 or less does not limit.
type Limiter struct {
	mu   sync.Mutex
	rate map[string]float64
	tok  map[string]float64
	at   map[string]time.Time
	now  func() time.Time
}

func New() *Limiter {
	return &Limiter{
		rate: map[string]float64{},
		tok:  map[string]float64{},
		at:   map[string]time.Time{},
		now:  time.Now,
	}
}

func (l *Limiter) SetNow(now func() time.Time) {
	if now != nil {
		l.now = now
	}
}

// Allow consumes one token for key at perSecond. perSecond <= 0 always allows.
func (l *Limiter) Allow(key string, perSecond float64) bool {
	if perSecond <= 0 || key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.rate[key] != perSecond {
		l.rate[key] = perSecond
		l.tok[key] = perSecond
		l.at[key] = now
	}
	elapsed := now.Sub(l.at[key]).Seconds()
	l.at[key] = now
	tokens := l.tok[key] + elapsed*perSecond
	if tokens > perSecond {
		tokens = perSecond
	}
	if tokens < 1 {
		l.tok[key] = tokens
		return false
	}
	l.tok[key] = tokens - 1
	return true
}

package group

import (
	"sync"
	"sync/atomic"
	"time"
)

// smartRate samples fixed one-second windows without a goroutine per connection.
// Short transfers use the same denominator, so a single burst is not inflated by
// the duration of a syscall. Idle lifetime does not dilute the observed peak.
type smartRate struct {
	access  sync.Mutex
	window  time.Time
	bytes   int64
	maximum int64
}

func (r *smartRate) add(now time.Time, n int64) {
	r.access.Lock()
	defer r.access.Unlock()
	if r.window.IsZero() || now.Sub(r.window) >= time.Second {
		r.window = now
		r.bytes = 0
	}
	r.bytes += n
	r.maximum = max(r.maximum, r.bytes)
}

func (r *smartRate) peak() float64 {
	r.access.Lock()
	defer r.access.Unlock()
	return float64(r.maximum)
}

type smartTransferCounter struct {
	atomic.Int64
	rate smartRate
}

func (c *smartTransferCounter) Add(n int64) int64 {
	if n > 0 {
		c.rate.add(time.Now(), n)
	}
	return c.Int64.Add(n)
}

func (c *smartTransferCounter) Peak() float64 { return c.rate.peak() }

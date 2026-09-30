package group

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

const (
	smartPinTTL              = 10 * time.Minute
	smartPinLimit            = 4096
	smartDeterministicPrefix = 3
	smartParallelDials       = 5
)

type smartPin struct {
	outbound adapter.Outbound
	used     time.Time
}
type smartSelection struct {
	access       sync.Mutex
	pins         map[string]smartPin
	failureStart time.Time
	failureCount int
}

func (p *smartSelection) clear() {
	p.access.Lock()
	defer p.access.Unlock()
	clear(p.pins)
	p.failureStart = time.Time{}
	p.failureCount = 0
}

func (p *smartSelection) pin(key string, outbound adapter.Outbound) {
	p.access.Lock()
	defer p.access.Unlock()
	if p.pins == nil {
		p.pins = make(map[string]smartPin)
	}
	if len(p.pins) >= smartPinLimit {
		var oldest string
		var age time.Time
		for key, pin := range p.pins {
			if age.IsZero() || pin.used.Before(age) {
				oldest, age = key, pin.used
			}
		}
		delete(p.pins, oldest)
	}
	p.pins[key] = smartPin{outbound: outbound, used: time.Now()}
}

func (p *smartSelection) order(key string, candidates []smartCandidate) ([]smartCandidate, bool) {
	p.access.Lock()
	defer p.access.Unlock()
	pin, exists := p.pins[key]
	if !exists {
		return candidates, false
	}
	if time.Since(pin.used) < smartPinTTL {
		for index, candidate := range candidates {
			if candidate.outbound == pin.outbound && !candidate.status.Blocked {
				result := slices.Clone(candidates)
				copy(result[1:index+1], result[:index])
				result[0] = candidate
				return result, true
			}
		}
	}
	delete(p.pins, key)
	return candidates, false
}

// acceptObservation prevents a disconnect burst from feeding the store and
// collector indefinitely. Success or a quiet window restores normal learning.
func (p *smartSelection) acceptObservation(now time.Time, success bool) (accept, resetBreakers bool) {
	p.access.Lock()
	defer p.access.Unlock()
	if success || now.Sub(p.failureStart) >= 2*time.Second {
		p.failureStart = now
		p.failureCount = 0
	}
	if success {
		return true, false
	}
	if p.failureStart.IsZero() {
		p.failureStart = now
	}
	p.failureCount++
	return p.failureCount < 50, p.failureCount == 50
}

func dialSmartConnection[T interface{ Close() error }](s *Smart, ctx context.Context, network string, target string, candidates []smartCandidate, dial func(context.Context, smartCandidate) (T, error)) (T, smartCandidate, time.Duration, error) {
	var zero T
	pinKey := network + "\x00" + target
	ordered, pinned := s.selection.order(pinKey, candidates)
	limit := s.maxSelected
	if limit <= 0 {
		limit = defaultSmartMaxSelected
	}
	limit = min(limit, len(ordered))
	var errs []error
	for index := 0; index < len(ordered); {
		if ctx.Err() != nil {
			return zero, smartCandidate{}, 0, ctx.Err()
		}
		count := 1
		if network == "tcp" && !pinned && index >= smartDeterministicPrefix && index < limit {
			count = min(smartParallelDials, limit-index)
		}
		batch := ordered[index : index+count]
		var timeout time.Duration
		for _, candidate := range batch {
			budget := 5 * time.Second
			if input, ok := s.store.ModelInput(candidate.status.Key); ok && input.ConnectTime > 0 {
				budget = min(budget, max(100*time.Millisecond, input.ConnectTime*5))
			}
			// A fast member must not shorten a slower or unknown peer's budget.
			timeout = max(timeout, budget)
		}
		if deadline, ok := ctx.Deadline(); ok {
			timeout = min(timeout, time.Until(deadline)/time.Duration(max(1, len(ordered)-index)))
		}
		attempt, cancel := context.WithTimeout(ctx, timeout)
		conn, winner, elapsed, err := raceSmartConnection(s, ctx, attempt, batch, dial)
		cancel()
		if err == nil {
			s.selection.pin(pinKey, winner.outbound)
			return conn, winner, elapsed, nil
		}
		if ctx.Err() != nil {
			return zero, smartCandidate{}, 0, ctx.Err()
		}
		errs = append(errs, err)
		index += count
	}
	return zero, smartCandidate{}, 0, errors.Join(errs...)
}

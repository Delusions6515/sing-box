package smart

import "time"

// ResetBreakers releases runtime quarantines after a group-wide failure flood.
// Learned metrics remain intact; the flood is not evidence against every node.
func (s *Store) ResetBreakers() {
	s.access.Lock()
	defer s.access.Unlock()
	for _, entry := range s.metrics {
		entry.ConsecutiveFailures = 0
		entry.BlockedUntil = time.Time{}
	}
	s.revision++
}

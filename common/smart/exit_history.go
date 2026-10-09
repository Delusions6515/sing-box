package smart

import (
	"sort"
	"time"
)

func (s *Store) ExitState(node string) (ExitNodeState, bool) {
	s.access.RLock()
	defer s.access.RUnlock()
	state, ok := s.exits[node]
	return state, ok
}

func (s *Store) SetExitState(node string, state ExitNodeState) {
	s.access.Lock()
	defer s.access.Unlock()
	if s.exits == nil {
		s.exits = make(map[string]ExitNodeState)
	}
	s.exits[node] = state
	s.exits = pruneExitNodes(s.exits, time.Time{}, 0, s.config.MaxEntries)
	s.revision++
}

func pruneExitNodes(states map[string]ExitNodeState, now time.Time, retention time.Duration, limit int) map[string]ExitNodeState {
	if len(states) == 0 {
		return nil
	}
	type item struct {
		node  string
		state ExitNodeState
	}
	entries := make([]item, 0, len(states))
	for node, state := range states {
		used := max(state.Updated, state.Failed)
		if node == "" || used == 0 || retention > 0 && now.Sub(time.Unix(used, 0)) > retention {
			continue
		}
		entries = append(entries, item{node, state})
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := max(entries[i].state.Updated, entries[i].state.Failed), max(entries[j].state.Updated, entries[j].state.Failed)
		if left != right {
			return left > right
		}
		return entries[i].node < entries[j].node
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	result := make(map[string]ExitNodeState, len(entries))
	for _, entry := range entries {
		result[entry.node] = entry.state
	}
	return result
}

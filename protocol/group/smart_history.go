package group

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sagernet/sing-box/common/smart"
	"github.com/sagernet/sing/service/filemanager"
)

const smartHistoryVersion = 1

type smartHistoryFile struct {
	Version int                       `json:"version"`
	Groups  map[string]smart.Snapshot `json:"groups"`
}

type smartHistoryEntry struct {
	access     sync.Mutex
	loaded     bool
	groups     map[string]smart.Snapshot
	references int
}

var smartHistoryPool = struct {
	sync.Mutex
	entries map[string]*smartHistoryEntry
}{entries: make(map[string]*smartHistoryEntry)}

func (s *Smart) loadHistory() error {
	if s.historyEntry != nil {
		return nil
	}
	poolKey := s.smartHistoryPoolKey()
	entry := acquireSmartHistory(poolKey)
	entry.access.Lock()
	if !entry.loaded {
		content, err := filemanager.ReadFile(s.ctx, s.historyPath)
		switch {
		case err == nil:
			var history smartHistoryFile
			if err = json.Unmarshal(content, &history); err != nil || history.Version != smartHistoryVersion {
				if err == nil {
					err = errors.New("unsupported smart history version")
				}
				s.warnSmartHistory("decode smart history: ", err)
			} else if history.Groups != nil {
				entry.groups = history.Groups
			}
			// Corrupt and unsupported files intentionally cold-start, matching the
			// documented behavior. They are safe to replace with a fresh snapshot.
			entry.loaded = true
		case errors.Is(err, os.ErrNotExist):
			entry.loaded = true
		default:
			entry.access.Unlock()
			releaseSmartHistory(poolKey, entry)
			return err
		}
	}
	snapshot, restored := entry.groups[s.Tag()]
	if restored {
		snapshot = smart.PruneSnapshot(snapshot, time.Now(), s.historyRetention, s.maxHistoryEntries)
		entry.groups[s.Tag()] = snapshot
	}
	entry.access.Unlock()
	if restored {
		if s.store.HasPendingChanges() {
			if !s.store.Merge(snapshot) {
				s.warnSmartHistory("ignore unsupported smart group history for ", s.Tag())
			}
		} else if !s.store.Restore(snapshot) {
			s.warnSmartHistory("ignore unsupported smart group history for ", s.Tag())
		}
		s.store.GC(time.Now(), s.historyRetention, s.maxHistoryEntries)
	}
	s.historyEntry = entry
	return nil
}

func (s *Smart) smartHistoryPoolKey() string {
	if s.historyPoolKey != "" {
		return s.historyPoolKey
	}
	return s.historyPath
}

func acquireSmartHistory(path string) *smartHistoryEntry {
	smartHistoryPool.Lock()
	defer smartHistoryPool.Unlock()
	entry := smartHistoryPool.entries[path]
	if entry == nil {
		entry = &smartHistoryEntry{groups: make(map[string]smart.Snapshot)}
		smartHistoryPool.entries[path] = entry
	}
	entry.references++
	return entry
}

func releaseSmartHistory(path string, entry *smartHistoryEntry) {
	smartHistoryPool.Lock()
	defer smartHistoryPool.Unlock()
	if smartHistoryPool.entries[path] != entry || entry.references == 0 {
		return
	}
	entry.references--
	if entry.references == 0 {
		delete(smartHistoryPool.entries, path)
	}
}

func (s *Smart) releaseHistory() {
	entry := s.historyEntry
	if entry == nil {
		return
	}
	s.historyEntry = nil
	releaseSmartHistory(s.smartHistoryPoolKey(), entry)
}

func (s *Smart) flushHistory(force bool) error {
	if !force && !s.store.HasPendingChanges() {
		return nil
	}
	if s.historyEntry == nil {
		if err := s.loadHistory(); err != nil {
			return err
		}
	}
	if !s.store.HasPendingChanges() {
		return nil
	}
	entry := s.historyEntry
	now := time.Now()
	snapshot, revision := s.store.SnapshotAndRevision(now, s.historyRetention, s.maxHistoryEntries)
	entry.access.Lock()
	defer entry.access.Unlock()
	if !entry.loaded {
		return errors.New("smart history has not loaded")
	}
	entry.groups[s.Tag()] = snapshot
	content, err := json.Marshal(smartHistoryFile{Version: smartHistoryVersion, Groups: entry.groups})
	if err != nil {
		return err
	}
	if err = filemanager.MkdirAll(s.ctx, filepath.Dir(s.historyPath), 0o755); err == nil {
		err = filemanager.WriteFile(s.ctx, s.historyPath+".tmp", content, 0o600)
	}
	if err == nil {
		err = filemanager.Rename(s.ctx, s.historyPath+".tmp", s.historyPath)
	}
	if err != nil {
		return err
	}
	s.store.MarkFlushed(revision)
	return nil
}

func (s *Smart) warnSmartHistory(args ...any) {
	if s.logger != nil {
		s.logger.Warn(args...)
	}
}

func (s *Smart) errorSmartHistory(args ...any) {
	if s.logger != nil {
		s.logger.Error(args...)
	}
}

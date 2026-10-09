package group

import "sync"

// memberUpdates invokes callbacks outside its lock. Callers must publish members
// and release their own locks before notifying, so observers may read All().
type memberUpdates struct {
	access    sync.Mutex
	callbacks map[*func()]func()
}

func (u *memberUpdates) RegisterMemberUpdateCallback(callback func()) func() {
	u.access.Lock()
	if u.callbacks == nil {
		u.callbacks = make(map[*func()]func())
	}
	token := &callback
	u.callbacks[token] = callback
	u.access.Unlock()
	return func() {
		u.access.Lock()
		delete(u.callbacks, token)
		u.access.Unlock()
	}
}

func (u *memberUpdates) notifyMembersUpdated() {
	u.access.Lock()
	callbacks := make([]func(), 0, len(u.callbacks))
	for _, callback := range u.callbacks {
		callbacks = append(callbacks, callback)
	}
	u.access.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

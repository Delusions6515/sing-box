package daemon

import (
	"context"
	"time"

	"github.com/sagernet/sing-box/common/configscript"
	"github.com/sagernet/sing/service"
)

const defaultAttachedLogMaxLines = 3000

// StartOrReloadService and CloseService must not be called on an attached service.
func NewAttachedService(ctx context.Context) *StartedService {
	instance := attachInstance(ctx)
	s := NewStartedService(ServiceOptions{
		Context:          ctx,
		ConfigScriptHost: service.FromContext[configscript.Host](ctx),
		LogMaxLines:      defaultAttachedLogMaxLines,
	})
	s.instance = instance
	s.serviceStatus = &ServiceStatus{Status: ServiceStatus_STARTED}
	s.startedAt = time.Now()
	instance.urlTestHistoryStorage.AddUpdateHook(s.urlTestSubscriber)
	if instance.clashMode != nil {
		instance.clashMode.AddUpdateHook(s.clashModeSubscriber)
	}
	instance.logFactory.AttachPlatformWriter(s)
	return s
}

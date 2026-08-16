package option

import (
	"context"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
	"github.com/stretchr/testify/require"
)

type smartOptionsRegistry struct{}

func (smartOptionsRegistry) OptionTypes() []string {
	return []string{C.TypeSmart}
}

func (smartOptionsRegistry) CreateOptions(outboundType string) (any, bool) {
	if outboundType == C.TypeSmart {
		return new(SmartOutboundOptions), true
	}
	return nil, false
}

func TestSmartOutboundOptionsRejectExcludedFields(t *testing.T) {
	ctx := service.ContextWith[OutboundOptionsRegistry](context.Background(), smartOptionsRegistry{})
	for _, field := range []string{"policy_priority", "use_lightgbm", "interrupt_exist_connections"} {
		t.Run(field, func(t *testing.T) {
			var outbound Outbound
			err := json.UnmarshalContext(ctx, []byte(`{"type":"smart","`+field+`":true}`), &outbound)
			require.ErrorContains(t, err, "unknown field")
		})
	}
}

package clashapi

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/trafficcontrol"
	"github.com/stretchr/testify/require"
)

func TestConnectionObjectUsesSmartConnectionWinner(t *testing.T) {
	winner := new(atomic.Pointer[adapter.Outbound])
	connection := connectionObject(trafficcontrol.TrackerMetadata{
		Metadata: adapter.InboundContext{SelectedOutbound: winner},
		Chain:    []string{"smart", "Telegram"},
		Upload:   new(atomic.Int64), Download: new(atomic.Int64),
	})
	var leaf adapter.Outbound = &connectionTestOutbound{tag: "actual-node"}
	winner.Store(&leaf)
	response, err := connection.MarshalJSON()
	require.NoError(t, err)
	var content struct {
		Chains []string `json:"chains"`
	}
	require.NoError(t, json.Unmarshal(response, &content))
	require.Equal(t, []string{"actual-node", "smart", "Telegram"}, content.Chains)
}

type connectionTestOutbound struct {
	adapter.Outbound
	tag string
}

func (o *connectionTestOutbound) Tag() string { return o.tag }

func TestConnectionObjectPreferAndroidPackageName(t *testing.T) {
	connection := connectionObject(trafficcontrol.TrackerMetadata{
		Metadata: adapter.InboundContext{
			ProcessInfo: &adapter.ConnectionOwner{
				UserId:       -1,
				ProcessPaths: []string{"/system/bin/app_process64"},
				PackageNames: []string{"io.nekohasekai.sfa"},
			},
		},
		Upload:   new(atomic.Int64),
		Download: new(atomic.Int64),
	})
	response, err := connection.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var content struct {
		Metadata struct {
			ProcessPath string `json:"processPath"`
		} `json:"metadata"`
	}
	err = json.Unmarshal(response, &content)
	if err != nil {
		t.Fatal(err)
	}
	if content.Metadata.ProcessPath != "io.nekohasekai.sfa" {
		t.Fatalf("unexpected process path: %s", content.Metadata.ProcessPath)
	}
}

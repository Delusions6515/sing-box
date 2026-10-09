package urltest

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/stretchr/testify/require"
)

func TestDelayHistoryKeepsResultsBySource(t *testing.T) {
	history := NewHistoryStorage()
	base := time.Now()
	history.StoreURLTestHistory("node", &adapter.URLTestHistory{Time: base, Delay: 10})
	history.StoreGroupURLTestHistory("node", &adapter.URLTestHistory{Time: base.Add(time.Second), Delay: 20})
	history.StoreSmartURLTestHistory("node", &adapter.URLTestHistory{Time: base.Add(2 * time.Second), Delay: 30})
	require.Equal(t, uint16(30), history.LoadURLTestHistory("node").Delay)

	history.DeleteSmartURLTestHistory("node")
	require.Equal(t, uint16(20), history.LoadURLTestHistory("node").Delay)
	history.DeleteGroupURLTestHistory("node")
	require.Equal(t, uint16(10), history.LoadURLTestHistory("node").Delay)
	history.DeleteURLTestHistory("node")
	require.Nil(t, history.LoadURLTestHistory("node"))
}

func TestDelayHistoryNewerManualResultSurvivesAutomaticFailure(t *testing.T) {
	history := NewHistoryStorage()
	base := time.Now()
	history.StoreSmartURLTestHistory("node", &adapter.URLTestHistory{Time: base, Delay: 20})
	history.StoreURLTestHistory("node", &adapter.URLTestHistory{Time: base.Add(time.Second), Delay: 42})
	history.DeleteSmartURLTestHistory("node")
	require.Equal(t, uint16(42), history.LoadURLTestHistory("node").Delay)
}

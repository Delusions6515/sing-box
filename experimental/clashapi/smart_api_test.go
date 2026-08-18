package clashapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

type smartOutboundManager struct {
	adapter.OutboundManager
	outbounds []adapter.Outbound
}

func (m *smartOutboundManager) Outbounds() []adapter.Outbound { return m.outbounds }

type plainOutbound struct {
	adapter.Outbound
	tag string
}

func (o *plainOutbound) Tag() string { return o.tag }

func TestGetGroupWeights(t *testing.T) {
	detour := &smartStatusTestGroup{status: adapter.SmartGroupStatus{
		Candidates: []adapter.SmartCandidateStatus{{Tag: "fast", Weight: 0.9}, {Tag: "slow", Weight: 0.1}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/group/smart/weights", nil)
	request = request.WithContext(context.WithValue(request.Context(), CtxKeyProxy, detour))
	response := httptest.NewRecorder()
	getGroupWeights(&Server{})(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{
		"weights": [
			{"Name": "fast", "Rank": "", "Weight": 0.9},
			{"Name": "slow", "Rank": "", "Weight": 0.1}
		]
	}`, response.Body.String())
}

func TestGetGroupWeightsRejectsNonSmartGroup(t *testing.T) {
	detour := &plainOutbound{tag: "plain"}
	request := httptest.NewRequest(http.MethodGet, "/group/plain/weights", nil)
	request = request.WithContext(context.WithValue(request.Context(), CtxKeyProxy, detour))
	response := httptest.NewRecorder()
	getGroupWeights(&Server{})(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.JSONEq(t, `{"weights": [], "error": "Not a Smart group"}`, response.Body.String())
}

func TestGetGroupWeightsEmpty(t *testing.T) {
	detour := &smartStatusTestGroup{status: adapter.SmartGroupStatus{}}
	request := httptest.NewRequest(http.MethodGet, "/group/smart/weights", nil)
	request = request.WithContext(context.WithValue(request.Context(), CtxKeyProxy, detour))
	response := httptest.NewRecorder()
	getGroupWeights(&Server{})(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"weights": [], "message": "No weight data available for the specified group"}`, response.Body.String())
}

func TestGetAllGroupWeights(t *testing.T) {
	smart := &smartStatusTestGroup{status: adapter.SmartGroupStatus{
		Candidates: []adapter.SmartCandidateStatus{{Tag: "fast", Weight: 0.9}},
	}}
	server := &Server{outbound: &smartOutboundManager{outbounds: []adapter.Outbound{
		smart,
		&plainOutbound{tag: "plain"},
	}}}
	request := httptest.NewRequest(http.MethodGet, "/group/weights", nil)
	response := httptest.NewRecorder()
	getAllGroupWeights(server)(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{
		"weights": {"smart": [{"Name": "fast", "Rank": "", "Weight": 0.9}]},
		"errors": {}
	}`, response.Body.String())
}

func TestGetAllGroupWeightsEmpty(t *testing.T) {
	server := &Server{outbound: &smartOutboundManager{outbounds: []adapter.Outbound{
		&plainOutbound{tag: "plain"},
	}}}
	request := httptest.NewRequest(http.MethodGet, "/group/weights", nil)
	response := httptest.NewRecorder()
	getAllGroupWeights(server)(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{
		"weights": {},
		"message": "No Smart groups or no weight data available"
	}`, response.Body.String())
}

func TestFlushSmartCache(t *testing.T) {
	smart := &smartStatusTestGroup{status: adapter.SmartGroupStatus{
		Candidates: []adapter.SmartCandidateStatus{{Tag: "fast"}},
	}}
	ctx := service.ContextWith[adapter.OutboundManager](context.Background(), &smartOutboundManager{outbounds: []adapter.Outbound{smart}})
	router := cacheRouter(ctx)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/smart/flush", nil))
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Empty(t, smart.status.Candidates)

	smart.status.Candidates = []adapter.SmartCandidateStatus{{Tag: "fast"}}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/smart/flush/myconfig", nil))
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Empty(t, smart.status.Candidates)
}

func TestFlushSmartCacheRequiresConfigName(t *testing.T) {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("config", "")
	request := httptest.NewRequest(http.MethodPost, "/cache/smart/flush/", nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	response := httptest.NewRecorder()
	flushSmartConfigCache(context.Background())(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.JSONEq(t, `{"message": "config name is required"}`, response.Body.String())
}

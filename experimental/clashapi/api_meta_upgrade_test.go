package clashapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagernet/sing-box/common/smartservice"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestUpgradeLightGBMUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	upgradeRouter(&Server{ctx: context.Background()}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/lgbm", nil))

	require.Equal(t, http.StatusNotFound, response.Code)
	require.JSONEq(t, `{"message":"Smart service not enabled"}`, response.Body.String())
}

func TestUpgradeLightGBMRejectsUnreadyService(t *testing.T) {
	logger := log.NewNOPFactory().NewLogger("test")
	ctx := service.ContextWith[*smartservice.Service](context.Background(), smartservice.NewService(context.Background(), logger, option.SmartOptions{}))
	response := httptest.NewRecorder()
	updateLightGBM(&Server{ctx: ctx, logger: logger})(response, httptest.NewRequest(http.MethodPost, "/lgbm", nil))

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.JSONEq(t, `{"message":"Smart service is not ready"}`, response.Body.String())
}

func TestUpgradeLightGBMRequiresAuthentication(t *testing.T) {
	router := chi.NewRouter()
	router.Use(authentication("secret"))
	router.Mount("/upgrade", upgradeRouter(&Server{ctx: context.Background()}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/upgrade/lgbm", nil))
	require.Equal(t, http.StatusUnauthorized, response.Code)

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/upgrade/lgbm", nil)
	request.Header.Set("Authorization", "Bearer secret")
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
}

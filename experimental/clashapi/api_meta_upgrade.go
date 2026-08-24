package clashapi

import (
	"errors"
	"net/http"

	"github.com/sagernet/sing-box/common/smartservice"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func upgradeRouter(server *Server) http.Handler {
	r := chi.NewRouter()
	r.Post("/ui", updateExternalUI(server))
	r.Post("/lgbm", updateLightGBM(server))
	return r
}

func updateExternalUI(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if server.externalUI == "" {
			render.Status(r, http.StatusNotFound)
			render.JSON(w, r, newError("external UI not enabled"))
			return
		}
		server.logger.Info("upgrading external UI")
		err := server.checkAndDownloadExternalUI(true)
		if err != nil {
			server.logger.Error(E.Cause(err, "upgrade external UI"))
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		render.JSON(w, r, render.M{"status": "ok"})
	}
}

func updateLightGBM(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		smartService := service.FromContext[*smartservice.Service](server.ctx)
		if smartService == nil {
			render.Status(r, http.StatusNotFound)
			render.JSON(w, r, newError("Smart service not enabled"))
			return
		}
		server.logger.Info("upgrading LightGBM model")
		err := smartService.UpdateModel(r.Context())
		if err != nil {
			server.logger.Error(E.Cause(err, "upgrade LightGBM model"))
			if errors.Is(err, smartservice.ErrModelUpdateInProgress) {
				render.Status(r, http.StatusConflict)
			} else {
				render.Status(r, http.StatusInternalServerError)
			}
			render.JSON(w, r, newError(err.Error()))
			return
		}
		render.JSON(w, r, render.M{"status": "ok"})
	}
}

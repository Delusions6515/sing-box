package clashapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func cacheRouter(ctx context.Context) http.Handler {
	r := chi.NewRouter()
	r.Post("/fakeip/flush", flushFakeip(ctx))
	r.Post("/dns/flush", flushDNS(ctx))
	r.Post("/smart/flush", flushAllSmartCache(ctx))
	r.Post("/smart/flush/{config}", flushSmartConfigCache(ctx))
	return r
}

func flushFakeip(ctx context.Context) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		cacheFile := service.FromContext[adapter.CacheFile](ctx)
		if cacheFile != nil {
			err := cacheFile.FakeIPReset()
			if err != nil {
				render.Status(r, http.StatusInternalServerError)
				render.JSON(w, r, newError(err.Error()))
				return
			}
		}
		render.NoContent(w, r)
	}
}

func flushDNS(ctx context.Context) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		dnsRouter := service.FromContext[adapter.DNSRouter](ctx)
		if dnsRouter != nil {
			dnsRouter.ClearCache()
		}
		render.NoContent(w, r)
	}
}

func flushAllSmartCache(ctx context.Context) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := flushSmartCache(ctx); err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		render.NoContent(w, r)
	}
}

func flushSmartConfigCache(ctx context.Context) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if chi.URLParam(r, "config") == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, newError("config name is required"))
			return
		}
		// sing-box smart cache is not config-scoped: the running instance has a
		// single active config, so the config name is accepted for mihomo API
		// compatibility and all smart groups are flushed.
		if err := flushSmartCache(ctx); err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		render.NoContent(w, r)
	}
}

func flushSmartCache(ctx context.Context) error {
	outboundManager := service.FromContext[adapter.OutboundManager](ctx)
	if outboundManager == nil {
		return nil
	}
	var errs []error
	for _, detour := range outboundManager.Outbounds() {
		if smartGroup, isSmartGroup := detour.(adapter.SmartGroup); isSmartGroup {
			if err := smartGroup.ClearCache(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterRoutes_SupportManifest(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(&Deps{
		SupportManifest: func(ctx context.Context) (json.RawMessage, error) {
			_ = ctx
			return json.RawMessage(`{"flow_id":"device-go","blocks":{"task_menu":{"title":{"text":"Task menu"}}}}`), nil
		},
	})(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/support-manifest", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != `{"flow_id":"device-go","blocks":{"task_menu":{"title":{"text":"Task menu"}}}}` {
		t.Fatalf("body = %q", got)
	}
}

func TestRegisterRoutes_OnboardingManifest(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(&Deps{
		OnboardingManifest: func(ctx context.Context) (json.RawMessage, error) {
			_ = ctx
			return json.RawMessage(`{"version":"2026-03-31.1","locales":{"zh":{"title":"新手指南"}}}`), nil
		},
	})(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/onboarding-manifest", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
		t.Fatalf("cache-control = %q, want no-cache, must-revalidate", got)
	}
	if got := rec.Body.String(); got != `{"version":"2026-03-31.1","locales":{"zh":{"title":"新手指南"}}}` {
		t.Fatalf("body = %q", got)
	}
}

func TestRegisterRoutes_OnboardingManifestProviderError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(&Deps{
		OnboardingManifest: func(ctx context.Context) (json.RawMessage, error) {
			_ = ctx
			return nil, errors.New("manifest unavailable")
		},
	})(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/onboarding-manifest", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got := body["error"]; got != "manifest unavailable" {
		t.Fatalf("error = %q, want manifest unavailable", got)
	}
}

func TestRegisterRoutes_IndexIncludesOnboardingDrawerShell(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, token := range []string{
		`<template x-if="showOnboardingDrawer">`,
		`<aside class="onboarding-drawer" x-ref="onboardingDrawer"`,
		`class="agent-onboarding-btn" x-ref="onboardingTrigger" @click="openOnboardingDrawer()"`,
		`async loadOnboardingManifest(force)`,
		`const resp = await fetch('/ui/api/onboarding-manifest', { headers });`,
		`throw new Error('invalid onboarding manifest');`,
		`@keydown.tab.prevent="cycleOnboardingFocus($event)"`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_IndexIncludesOnboardingInteractionHelpers(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, token := range []string{
		"insertOnboardingCommand(command)",
		"defaultOnboardingManifest()",
		"resolvedOnboardingManifest()",
		"onboarding-command-btn",
		"in (group.items || [])",
		`x-text="onboardingText(item.label, item.command || '')"`,
		"onboardingLoadFailed: false",
		"_onboardingReturnFocus: null",
		"onboardingFocusableElements()",
		"focusOnboardingDrawer()",
		"cycleOnboardingFocus(e)",
		"restoreOnboardingFocus()",
		"if (this.onboardingLoadFailed) return this.defaultOnboardingManifest();",
		"this._onboardingReturnFocus = document.activeElement",
		"this.mobileTab = 'chat';",
		"restoreTarget && restoreTarget.isConnected",
		"if (this.showOnboardingDrawer)",
		"key === 'escape'",
		"key === 'k'",
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("body missing %q", token)
		}
	}
}

func TestRegisterRoutes_FaviconAssets(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(nil)(mux)

	t.Run("ui favicon svg", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ui/favicon.svg", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
			t.Fatalf("content-type = %q, want image/svg+xml", got)
		}
	})

	t.Run("root favicon redirect", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
		}
		if got := rec.Header().Get("Location"); got != "/ui/favicon.ico" {
			t.Fatalf("location = %q, want /ui/favicon.ico", got)
		}
	})

	t.Run("apple touch icon png", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ui/apple-touch-icon.png", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Fatalf("content-type = %q, want image/png", got)
		}
	})
}

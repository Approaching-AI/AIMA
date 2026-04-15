package main

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/jguan/aima/internal/proxy"
)

func requireOnboardingMutation(ac *appContext, w http.ResponseWriter, r *http.Request) bool {
	if !sameOriginOnboardingRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if !jsonOnboardingRequest(r) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if ac != nil && ac.proxy != nil {
		if key := strings.TrimSpace(ac.proxy.APIKey()); key != "" && !proxy.CheckBearerAuth(r.Header.Get("Authorization"), key) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
	}
	return true
}

// requireOnboardingRead gates GET endpoints that expose hardware/stack state.
// It enforces same-origin + optional bearer auth but skips the JSON
// content-type check (GET requests carry no body). This prevents cross-origin
// reads (e.g. <img src> probes) from leaking hardware fingerprints.
func requireOnboardingRead(ac *appContext, w http.ResponseWriter, r *http.Request) bool {
	if !sameOriginOnboardingRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if ac != nil && ac.proxy != nil {
		if key := strings.TrimSpace(ac.proxy.APIKey()); key != "" && !proxy.CheckBearerAuth(r.Header.Get("Authorization"), key) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
	}
	return true
}

func sameOriginOnboardingRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func jsonOnboardingRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}

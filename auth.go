package main

import (
	"io"
	"net/http"
	"strings"
	"time"
)

func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request) bool {
	cfg := g.cfg.Auth
	if cfg.IdentityHeader == "" {
		return true
	}
	if r.Header.Get(cfg.IdentityHeader) != "" {
		return true
	}
	if cfg.VerifyURL == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return false
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cfg.VerifyURL, nil)
	if err != nil {
		http.Error(w, "authentication unavailable", http.StatusBadGateway)
		return false
	}
	for k, values := range r.Header {
		for _, value := range values {
			req.Header.Add(k, value)
		}
	}
	req.Header.Set("X-Forwarded-Method", r.Method)
	req.Header.Set("X-Forwarded-Uri", r.URL.RequestURI())
	req.Header.Set("X-Forwarded-Host", r.Host)
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	req.Header.Set("X-Forwarded-Proto", proto)
	timeout := cfg.Timeout.Duration
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "authentication unavailable", http.StatusBadGateway)
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		for _, name := range []string{"Location", "Set-Cookie", "Retry-After"} {
			for _, value := range resp.Header.Values(name) {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return false
	}
	for _, name := range append(append([]string{}, cfg.CopyHeaders...), cfg.IdentityHeader, cfg.GroupsHeader) {
		r.Header.Del(name)
	}
	for _, name := range cfg.CopyHeaders {
		if value := resp.Header.Get(name); value != "" {
			r.Header.Set(name, value)
		}
	}
	if r.Header.Get(cfg.IdentityHeader) == "" {
		http.Error(w, "authentication response omitted identity", http.StatusBadGateway)
		return false
	}
	return true
}

func (g *Gateway) authorizeApp(w http.ResponseWriter, r *http.Request, m Manifest) bool {
	if !g.authorize(w, r) {
		return false
	}
	if g.appAccessAllowed(r, m) {
		return true
	}
	http.Error(w, "application access denied", http.StatusForbidden)
	return false
}

func (g *Gateway) appAccessAllowed(r *http.Request, m Manifest) bool {
	if m.Access == "public" || g.cfg.Auth.IdentityHeader == "" || (len(m.AccessUsers) == 0 && len(m.AccessGroups) == 0) {
		return true
	}
	identity := r.Header.Get(g.cfg.Auth.IdentityHeader)
	for _, user := range m.AccessUsers {
		if identity == user {
			return true
		}
	}
	allowedGroups := make(map[string]bool, len(m.AccessGroups))
	for _, group := range m.AccessGroups {
		allowedGroups[group] = true
	}
	for _, value := range r.Header.Values(g.cfg.Auth.GroupsHeader) {
		for _, group := range strings.Split(value, ",") {
			if allowedGroups[strings.TrimSpace(group)] {
				return true
			}
		}
	}
	return false
}

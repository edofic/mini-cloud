package main

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request) bool {
	return g.authorizeURI(w, r, r.URL.RequestURI())
}

func (g *Gateway) authorizeURI(w http.ResponseWriter, r *http.Request, uri string) bool {
	cfg := g.cfg.Auth
	if isPublicIngress(r.Context()) && g.cfg.PublicIngress != nil && g.cfg.PublicIngress.VerifyURL != "" {
		cfg.VerifyURL = g.cfg.PublicIngress.VerifyURL
	}
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
		// Application bearer credentials and referrers are not browser login
		// credentials. Referrers can also contain OAuth callback secrets.
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Referer") {
			continue
		}
		for _, value := range values {
			req.Header.Add(k, value)
		}
	}
	req.Header.Set("X-Forwarded-Method", r.Method)
	req.Header.Set("X-Forwarded-Uri", uri)
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
	for _, cookie := range resp.Header.Values("Set-Cookie") {
		w.Header().Add("Set-Cookie", cookie)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		for _, name := range []string{"Location", "Retry-After"} {
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
	uri := r.URL.RequestURI()
	for _, p := range m.AuthRedactQueryPaths {
		if r.URL.Path == p {
			uri = r.URL.EscapedPath()
			break
		}
	}
	if !g.authorizeURI(w, r, uri) {
		return false
	}
	if g.appAccessAllowed(r, m) {
		return true
	}
	http.Error(w, "application access denied", http.StatusForbidden)
	return false
}

// Exceptions never forward identities, even from a trusted upstream: the app
// receives only its own API credentials on these routes.
func (g *Gateway) clearIdentity(r *http.Request) {
	for name := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "remote-") || strings.HasPrefix(lower, "remote_") {
			r.Header.Del(name)
		}
	}
	for _, name := range append(append([]string{}, g.cfg.Auth.CopyHeaders...), g.cfg.Auth.IdentityHeader, g.cfg.Auth.GroupsHeader) {
		r.Header.Del(name)
	}
}

func canonicalAuthPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\%?#") {
		return false
	}
	for _, c := range p {
		if c <= ' ' || c == 127 {
			return false
		}
	}
	return p == path.Clean(p) || p != "/" && p == path.Clean(p)+"/"
}

func validateAuthPaths(field string, patterns []string, wildcard bool) error {
	for _, pattern := range patterns {
		p := pattern
		if wildcard && strings.HasSuffix(p, "/*") {
			p = strings.TrimSuffix(p, "*")
		}
		if !canonicalAuthPath(p) || strings.Contains(p, "*") {
			return fmt.Errorf("%s: invalid canonical absolute path %q", field, pattern)
		}
	}
	return nil
}

func matchesPublicPath(r *http.Request, patterns []string) bool {
	// An escaped separator can change routing after another decoding pass.
	escaped := strings.ToLower(r.URL.EscapedPath())
	if !canonicalAuthPath(r.URL.Path) || strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return false
	}
	for _, pattern := range patterns {
		if r.URL.Path == pattern || strings.HasSuffix(pattern, "/*") && strings.HasPrefix(r.URL.Path, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
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

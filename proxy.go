package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"path/filepath"
	"time"
)

func (a *App) proxy(w http.ResponseWriter, r *http.Request, backend string) {
	a.mu.Lock()
	hardened := isPublicIngress(r.Context()) || a.manifest.Sandbox == hardenedSandbox
	a.mu.Unlock()
	proxy := httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme = "http"
			p.Out.URL.Host = backend
			p.Out.Host = p.In.Host
			// Incoming forwarding metadata is trusted only behind the documented edge proxy.
			p.Out.Header["X-Forwarded-For"] = append([]string(nil), p.In.Header.Values("X-Forwarded-For")...)
			p.SetXForwarded()
			if proto := p.In.Header.Get("X-Forwarded-Proto"); proto != "" {
				p.Out.Header.Set("X-Forwarded-Proto", proto)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "backend unavailable", http.StatusBadGateway)
		},
	}
	if hardened {
		socket := filepath.Join(a.gw.sandboxRoot, a.name, "backend", "app.sock")
		transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialSandboxBackend(ctx, socket)
		}}
		defer transport.CloseIdleConnections()
		proxy.Transport = transport
	}
	proxy.ServeHTTP(w, r)
}

func waitReadyUnix(ctx context.Context, socket string, cfg ReadinessConfig) error {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialSandboxBackend(ctx, socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for {
		if cfg.Type == "http" {
			path := cfg.Path
			if path == "" {
				path = "/"
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://backend"+path, nil)
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 400 {
					return nil
				}
			}
		} else {
			probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			conn, err := dialSandboxBackend(probeCtx, socket)
			cancel()
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness timeout: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

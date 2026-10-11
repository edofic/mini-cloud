package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

type publicIngressContextKey struct{}

func isPublicIngress(ctx context.Context) bool {
	return ctx.Value(publicIngressContextKey{}) == true
}

// The directory is provisioned by the deployment, never supplied by an app.
// Refuse existing files, symlinks and live sockets; only clean up crash residue.
func listenPublicIngress(path string) (net.Listener, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("public ingress path is not a socket")
		}
		conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("public ingress socket is already in use")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("check existing public ingress socket: %w", dialErr)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func (g *Gateway) servePublicHTTP(w http.ResponseWriter, r *http.Request) {
	if g.cfg.PublicIngress == nil {
		http.NotFound(w, r)
		return
	}
	name, allowed := g.cfg.PublicIngress.Hosts[stripPort(r.Host)]
	g.mu.RLock()
	a := g.apps[name]
	g.mu.RUnlock()
	if !allowed || a == nil {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	m := a.manifest
	a.mu.Unlock()
	if m.Sandbox != hardenedSandbox || m.Static != nil {
		http.Error(w, "public ingress requires a hardened process or CGI app", http.StatusServiceUnavailable)
		return
	}
	// Clone before modifying metadata. Public ingress never trusts edge identity,
	// even if the edge has already performed its own forward-auth check.
	r = r.Clone(context.WithValue(r.Context(), publicIngressContextKey{}, true))
	for name := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "remote-") || strings.HasPrefix(lower, "remote_") {
			r.Header.Del(name)
		}
	}
	for _, name := range append(append([]string{}, g.cfg.Auth.CopyHeaders...), g.cfg.Auth.IdentityHeader, g.cfg.Auth.GroupsHeader) {
		r.Header.Del(name)
	}
	r.Header.Del("Forwarded")
	r.Header.Del("X-Forwarded-Method")
	r.Header.Del("X-Forwarded-Uri")
	r.Header.Del("X-Forwarded-Host")
	r.Header.Set("X-Forwarded-Proto", "https")
	if !g.authorizeApp(w, r, m) {
		return
	}
	a.serveManifest(w, r, m)
}

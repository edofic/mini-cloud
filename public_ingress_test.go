package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicIngressAlwaysVerifiesIdentityAndAPIPaths(t *testing.T) {
	var calls atomic.Int32
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Remote-User") != "" || r.Header.Get("Remote_Groups") != "" || r.Header.Get("X-Identity") != "" {
			t.Error("edge-supplied identity reached verifier")
		}
		if r.Header.Get("X-Forwarded-Host") != "shopping.test" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Uri") != "/api/items" || r.Header.Get("X-Forwarded-Method") != "POST" {
			t.Error("verifier received forged request metadata")
		}
		switch r.Header.Get("Cookie") {
		case "session=good":
			w.Header().Set("X-Identity", "verified")
		case "session=bypass":
			// A policy bypass without an identity must never open public ingress.
		default:
			w.Header().Set("Location", "https://auth.test/")
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer verifier.Close()
	g := NewGateway(testConfig(t.TempDir()))
	g.ctx = t.Context()
	g.cfg.Auth = AuthConfig{IdentityHeader: "X-Identity", VerifyURL: verifier.URL, CopyHeaders: []string{"X-Identity"}}
	g.cfg.PublicIngress = &PublicIngressConfig{Hosts: map[string]string{"shopping.test": "shopping"}}
	a := &App{gw: g, name: "shopping", manifest: Manifest{Sandbox: hardenedSandbox, Access: "public", Process: &ProcessConfig{}}, manualStop: true}
	g.apps["shopping"] = a
	for _, tc := range []struct {
		cookie string
		status int
	}{
		{"", http.StatusUnauthorized},
		{"session=bypass", http.StatusBadGateway},
		{"session=good", http.StatusServiceUnavailable}, // Reached the intentionally stopped backend.
	} {
		r := httptest.NewRequest("POST", "http://shopping.test/api/items", strings.NewReader("payload"))
		r.Header.Set("Remote-User", "forged")
		r.Header.Set("Remote_Groups", "admin")
		r.Header.Set("X-Identity", "forged")
		r.Header.Set("X-Forwarded-Host", "bypass.test")
		r.Header.Set("X-Forwarded-Proto", "http")
		r.Header.Set("X-Forwarded-Uri", "/public")
		r.Header.Set("Cookie", tc.cookie)
		w := httptest.NewRecorder()
		g.servePublicHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("cookie=%q status=%d body=%s", tc.cookie, w.Code, w.Body)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("verifier calls=%d", calls.Load())
	}
	for _, host := range []string{"apps.test", "admin.test", "private.test"} {
		g.hosts[host] = &App{name: "private"}
		w := httptest.NewRecorder()
		g.servePublicHTTP(w, httptest.NewRequest("GET", "http://"+host+"/", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("host=%s status=%d", host, w.Code)
		}
	}
	a.manifest.Sandbox = "bubblewrap"
	w := httptest.NewRecorder()
	g.servePublicHTTP(w, httptest.NewRequest("GET", "http://shopping.test/", nil))
	if w.Code != http.StatusServiceUnavailable || calls.Load() != 3 {
		t.Fatal("weakened app was routed publicly")
	}
	a.manifest.Sandbox = hardenedSandbox
	a.manifest.Access = "authenticated"
	a.manifest.AccessUsers = []string{"another-user"}
	r := httptest.NewRequest("POST", "http://shopping.test/api/items", nil)
	r.Header.Set("Cookie", "session=good")
	w = httptest.NewRecorder()
	g.servePublicHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("principal restriction status=%d", w.Code)
	}
}

func TestPublicIngressAcquisitionRejectsWeakenedPolicy(t *testing.T) {
	g := NewGateway(testConfig(t.TempDir()))
	g.ctx = t.Context()
	a := &App{gw: g, manifest: Manifest{Sandbox: "bubblewrap", Process: &ProcessConfig{}}, state: "running"}
	ctx := context.WithValue(t.Context(), publicIngressContextKey{}, true)
	if _, _, err := a.acquire(ctx); err == nil {
		t.Fatal("public request acquired weak backend")
	}
}

func TestPublicIngressUsesSeparateVerifier(t *testing.T) {
	makeVerifier := func(cookie string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cookie") != cookie {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Remote-User", "verified")
		}))
	}
	private := makeVerifier("private=valid")
	defer private.Close()
	public := makeVerifier("public=valid")
	defer public.Close()
	g := NewGateway(testConfig(t.TempDir()))
	g.ctx = t.Context()
	g.cfg.Auth = AuthConfig{VerifyURL: private.URL, IdentityHeader: "Remote-User", CopyHeaders: []string{"Remote-User"}}
	g.cfg.PublicIngress = &PublicIngressConfig{VerifyURL: public.URL, Hosts: map[string]string{"shopping.test": "shopping"}}
	g.apps["shopping"] = &App{gw: g, name: "shopping", manifest: Manifest{Sandbox: hardenedSandbox, Access: "public", Process: &ProcessConfig{}}, manualStop: true}
	for _, cookie := range []string{"private=valid", "public=valid"} {
		r := httptest.NewRequest("GET", "http://shopping.test/api/items", nil)
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		g.servePublicHTTP(w, r)
		want := http.StatusUnauthorized
		if cookie == "public=valid" {
			want = http.StatusServiceUnavailable // Authenticated, reached stopped backend.
		}
		if w.Code != want {
			t.Fatalf("public cookie %q: status %d, want %d", cookie, w.Code, want)
		}
		r = httptest.NewRequest("GET", "http://shopping.private.test/", nil)
		r.Header.Set("Cookie", cookie)
		r.Header.Set("X-Forwarded-Host", "shopping.test") // Cannot select the public verifier.
		if got := g.authorize(httptest.NewRecorder(), r); got != (cookie == "private=valid") {
			t.Fatalf("private cookie %q: authorized %v", cookie, got)
		}
	}
}

// macOS temp directories plus Go's test-name prefix exceed the Unix socket
// path limit. Keep the socket in a short, private directory on both platforms.
func testPublicSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(dir, "public.sock")
}

func TestPublicIngressSocketDoesNotReplaceFilesOrActiveListeners(t *testing.T) {
	path := testPublicSocketPath(t)
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenPublicIngress(path); err == nil {
		t.Fatal("replaced a regular file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	listener, err := listenPublicIngress(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0660 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	if other, err := listenPublicIngress(path); err == nil {
		_ = other.Close()
		t.Fatal("replaced active socket")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := listenPublicIngress(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicIngressListenerSharesGatewayLifecycle(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Listen = "127.0.0.1:0"
	cfg.PublicIngress = &PublicIngressConfig{SocketPath: testPublicSocketPath(t), Hosts: map[string]string{"shopping.test": "shopping"}}
	g := NewGateway(cfg)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	defer cancel()
	waitFor(t, func() bool { _, err := os.Stat(cfg.PublicIngress.SocketPath); return err == nil }, "public socket did not start")
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.PublicIngress.SocketPath)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	resp, err := client.Get("http://admin.test/api/apps")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal("public listener exposed admin")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateway did not stop")
	}
	if _, err := os.Stat(cfg.PublicIngress.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("public socket survived shutdown")
	}
}

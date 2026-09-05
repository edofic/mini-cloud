package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticConfinement(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "public")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "direct", "fallback"} {
		if err := os.Symlink("../secret", filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, path string
		browse     bool
		fallback   string
	}{{"index", "/", false, ""}, {"browse index", "/", true, ""}, {"direct", "/direct", false, ""}, {"fallback", "/missing", false, "fallback"}} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{dir: dir}
			w := httptest.NewRecorder()
			a.serveStatic(w, httptest.NewRequest("GET", tc.path, nil), &StaticConfig{Root: "public", Browse: tc.browse, SPAFallback: tc.fallback})
			if strings.Contains(w.Body.String(), "outside-secret") || w.Code == 200 {
				t.Fatalf("escaped root: %d %s", w.Code, w.Body)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(root, "ok"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&App{dir: dir}).serveStatic(w, httptest.NewRequest("GET", "/link", nil), &StaticConfig{Root: "public"})
	if w.Code != 200 || w.Body.String() != "inside" {
		t.Fatalf("inside link: %d %s", w.Code, w.Body)
	}

	alias := filepath.Join(t.TempDir(), "app")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	(&App{dir: alias}).serveStatic(w, httptest.NewRequest("GET", "/ok", nil), &StaticConfig{Root: "public"})
	if w.Code != 200 || w.Body.String() != "inside" {
		t.Fatalf("symlinked app root: %d %s", w.Code, w.Body)
	}
}

func TestCGIHeaderBoundsAndStatus(t *testing.T) {
	for _, input := range []string{"Status: 0\n\n", "Status: 999\n\n", "Status: no\n\n", "Status: 101 Switching Protocols\n\n", "X-Large: " + strings.Repeat("x", 65<<10) + "\n\n"} {
		if _, _, err := readCGIHeaders(bufio.NewReader(strings.NewReader(input))); err == nil {
			t.Fatal("accepted invalid CGI headers")
		}
	}
	br := bufio.NewReader(strings.NewReader("Status: 201 Created\nContent-Type: text/plain\n\nbody"))
	_, status, err := readCGIHeaders(br)
	body, _ := io.ReadAll(br)
	if err != nil || status != 201 || string(body) != "body" {
		t.Fatalf("status=%d body=%q err=%v", status, body, err)
	}
}

func TestVerifierClearsStaleGroupsAndTimesOut(t *testing.T) {
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Remote-User", "alice")
	}))
	defer verifier.Close()
	g := &Gateway{cfg: Config{Auth: AuthConfig{IdentityHeader: "Remote-User", GroupsHeader: "Remote-Groups", VerifyURL: verifier.URL, CopyHeaders: []string{"Remote-User", "Remote-Groups"}, Timeout: Duration{20 * time.Millisecond}}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Remote-Groups", "admin")
	w := httptest.NewRecorder()
	if !g.authorize(w, r) || r.Header.Get("Remote-Groups") != "" {
		t.Fatalf("stale groups: %v %d", r.Header, w.Code)
	}
	g.cfg.Auth.VerifyURL += "/slow"
	w = httptest.NewRecorder()
	if g.authorize(w, httptest.NewRequest("GET", "/", nil)) || w.Code != 502 {
		t.Fatalf("timeout status=%d", w.Code)
	}
}

func TestAdminRequiresIdentityWithoutAdminRestriction(t *testing.T) {
	g := &Gateway{cfg: Config{Auth: AuthConfig{IdentityHeader: "Remote-User"}}}
	w := httptest.NewRecorder()
	g.serveAdmin(w, httptest.NewRequest("GET", "/api/apps", nil))
	if w.Code != 401 {
		t.Fatalf("anonymous admin status=%d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/apps", nil)
	r.Header.Set("Remote-User", "alice")
	w = httptest.NewRecorder()
	g.serveAdmin(w, r)
	if w.Code != 200 {
		t.Fatalf("authenticated admin status=%d", w.Code)
	}
}

func TestProxyStripsConnectionHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Secret") != "" {
			t.Error("forwarded connection-nominated request header")
		}
		if r.Header.Get("X-Forwarded-Proto") != "https" {
			t.Error("lost trusted scheme")
		}
		w.Header().Set("Connection", "X-Backend-Secret")
		w.Header().Set("X-Backend-Secret", "secret")
		w.WriteHeader(200)
	}))
	defer backend.Close()
	r := httptest.NewRequest("GET", "http://app.example.com/", nil)
	r.Header.Set("Connection", "X-Secret")
	r.Header.Set("X-Secret", "secret")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	(&App{}).proxy(w, r, strings.TrimPrefix(backend.URL, "http://"))
	if w.Code != 200 || w.Header().Get("X-Backend-Secret") != "" {
		t.Fatalf("response: %d %v", w.Code, w.Header())
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManifestAuthPathValidation(t *testing.T) {
	for _, p := range []string{"", "api", "/api/**", "/a/*/b", "/a/../api", "/api//", "/api?key=x", "/api#x", "/api%2f", "/api\\x", "/api\nx"} {
		t.Run(p, func(t *testing.T) {
			dir := t.TempDir()
			writeJSON(t, filepath.Join(dir, manifestName), Manifest{Static: &StaticConfig{}, PublicPaths: []string{p}})
			if _, err := loadManifest(dir); err == nil {
				t.Fatalf("accepted public path %q", p)
			}
		})
	}
	for _, m := range []Manifest{
		{Access: "public", PublicPaths: []string{"/api"}},
		{AuthRedactQueryPaths: []string{"/callback/*"}},
	} {
		dir := t.TempDir()
		m.Static = &StaticConfig{}
		writeJSON(t, filepath.Join(dir, manifestName), m)
		if _, err := loadManifest(dir); err == nil {
			t.Fatalf("accepted invalid auth paths: %+v", m)
		}
	}
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{Static: &StaticConfig{}, PublicPaths: []string{"/api", "/api/*", "/health", "/"}, AuthRedactQueryPaths: []string{"/callback"}})
	if _, err := loadManifest(dir); err != nil {
		t.Fatal(err)
	}
}

func TestPrivatePathExceptionsAndIdentityBoundary(t *testing.T) {
	var calls atomic.Int32
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer verifier.Close()
	g := NewGateway(testConfig(t.TempDir()))
	g.ctx = t.Context()
	g.cfg.Auth = AuthConfig{IdentityHeader: "X-Identity", GroupsHeader: "X-Groups", CopyHeaders: []string{"X-Identity", "X-Email"}, VerifyURL: verifier.URL}
	m := Manifest{Access: "authenticated", AccessUsers: []string{"alice"}, PublicPaths: []string{"/api", "/api/*", "/openapi.json"}, Process: &ProcessConfig{}}
	g.hosts["app.test"] = &App{gw: g, manifest: m, manualStop: true}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api", 503}, {"/api/items?x=y", 503}, {"/api/", 503}, {"/openapi.json", 503},
		{"/", 401}, {"/apiculture", 401}, {"/openapi.json/extra", 401}, {"/api%2fitems", 401},
		{"/api/../settings", 400}, {"/api//items", 400}, {"/api/%252e%252e/settings", 400}, {"/api/%5csettings", 400},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://app.test"+tc.path, strings.NewReader("body"))
			r.Header.Set("Authorization", "Bearer app-secret")
			r.Header.Set("Remote-User", "forged")
			r.Header.Set("Remote_Groups", "admin")
			r.Header.Set("X-Email", "forged@test")
			before := calls.Load()
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.status == 503 {
				if calls.Load() != before || r.Header.Get("Authorization") != "Bearer app-secret" {
					t.Fatal("exception verified or lost API credentials")
				}
				for _, name := range []string{"Remote-User", "Remote_Groups", "X-Email"} {
					if r.Header.Get(name) != "" {
						t.Fatalf("exception retained %s", name)
					}
				}
			}
		})
	}
	// Even a trusted browser identity cannot become an API identity.
	r := httptest.NewRequest("GET", "http://app.test/api", nil)
	r.Header.Set("X-Identity", "alice")
	r.Header.Set("X-Groups", "admin")
	g.ServeHTTP(httptest.NewRecorder(), r)
	if r.Header.Get("X-Identity") != "" || r.Header.Get("X-Groups") != "" {
		t.Fatal("exception retained trusted identity")
	}
}

func TestAppVerificationRedactsQueryAndPreservesSession(t *testing.T) {
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/callback"
		if r.Header.Get("X-Forwarded-Host") == "ordinary.test" {
			want = "/callback?code=secret&state=secret"
		}
		if r.Header.Get("X-Forwarded-Uri") != want || r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" {
			t.Errorf("unexpected verifier metadata: %+v", r.Header)
		}
		w.Header().Set("Remote-User", "alice")
		w.Header().Add("Set-Cookie", "session=renewed; Secure; HttpOnly")
		w.Header().Add("Set-Cookie", "other=value; Secure")
	}))
	defer verifier.Close()
	g := NewGateway(testConfig(t.TempDir()))
	g.cfg.Auth = AuthConfig{IdentityHeader: "Remote-User", CopyHeaders: []string{"Remote-User"}, VerifyURL: verifier.URL}
	for _, redact := range []bool{true, false} {
		m := Manifest{Access: "authenticated", AccessUsers: []string{"alice"}}
		host := "ordinary.test"
		if redact {
			m.AuthRedactQueryPaths = []string{"/callback"}
			host = "redacted.test"
		}
		r := httptest.NewRequest("GET", "https://"+host+"/callback?code=secret&state=secret", nil)
		r.Header.Set("Authorization", "Bearer app-secret")
		r.Header.Set("Referer", "https://app.test/callback?code=secret")
		w := httptest.NewRecorder()
		if !g.authorizeApp(w, r, m) {
			t.Fatal(w.Body.String())
		}
		if len(w.Result().Cookies()) != 2 || r.Header.Get("Remote-User") != "alice" || r.URL.RawQuery != "code=secret&state=secret" || r.Header.Get("Authorization") != "Bearer app-secret" {
			t.Fatal("lost session, verified identity, or original application request")
		}
	}
}

func TestInvalidAuthPathsKeepLastValidManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Static: &StaticConfig{}, PublicPaths: []string{"/api/*"}}
	writeJSON(t, filepath.Join(dir, manifestName), m)
	g := NewGateway(testConfig(root))
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	m.PublicPaths = []string{"/api/**"}
	writeJSON(t, filepath.Join(dir, manifestName), m)
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	a := g.apps["app"]
	if a.configError == "" || len(a.manifest.PublicPaths) != 1 || a.manifest.PublicPaths[0] != "/api/*" {
		t.Fatal("invalid edit replaced the active policy or hid its error")
	}
}

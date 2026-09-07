package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAppLinksPreserveBrowserSchemeAndPort(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "page")
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{Host: "custom.test", Static: &StaticConfig{Root: "public"}, Access: "public"})
	cfg := testConfig(root)
	cfg.IndexHost = "apps.test"
	g := NewGateway(cfg)
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{cfg.IndexHost, cfg.AdminHost} {
		for _, origin := range []string{"http://%s:9080/", "http://%s/", "https://%s/", "https://%s:8443/"} {
			pageURL := strings.ReplaceAll(origin, "%s", host)
			t.Run(pageURL, func(t *testing.T) {
				browserURL, err := url.Parse(pageURL)
				if err != nil {
					t.Fatal(err)
				}
				// Simulate plain HTTP from a TLS proxy too: the browser, rather
				// than the backend connection or forwarded headers, sets the scheme.
				r := httptest.NewRequest("GET", "http://"+browserURL.Host+"/", nil)
				w := httptest.NewRecorder()
				g.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				match := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(w.Body.String())
				if len(match) != 2 {
					t.Fatalf("missing app link: %s", w.Body.String())
				}
				link, err := url.Parse(match[1])
				if err != nil {
					t.Fatal(err)
				}
				got := browserURL.ResolveReference(link).String()
				want := strings.ReplaceAll(origin, "%s", "custom.test")
				if got != want {
					t.Fatalf("app URL=%q, want %q", got, want)
				}
			})
		}
	}
}

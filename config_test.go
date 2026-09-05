package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestRejectsPrincipalsOnPublicApp(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{
		Access:      "public",
		AccessUsers: []string{"alice"},
		Static:      &StaticConfig{Root: "public"},
	})
	_, err := loadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "require authenticated access") {
		t.Fatalf("loadManifest error=%v", err)
	}
}

func TestManifestNormalizesPrincipals(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{
		AccessUsers:  []string{" alice ", "alice"},
		AccessGroups: []string{" admin "},
		Static:       &StaticConfig{Root: "public"},
	})
	m, err := loadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.AccessUsers) != 1 || m.AccessUsers[0] != "alice" {
		t.Fatalf("access_users=%q", m.AccessUsers)
	}
	if len(m.AccessGroups) != 1 || m.AccessGroups[0] != "admin" {
		t.Fatalf("access_groups=%q", m.AccessGroups)
	}
}

func TestRejectInvalidManifestSettings(t *testing.T) {
	for _, body := range []string{
		`{"cgi":{"command":["true"],"maximum_concurrency":-1}}`,
		`{"cgi":{"command":["true"],"timeout":"-1s"}}`,
		`{"process":{"command":["true"],"readiness":{"type":"maybe"}}}`,
		`{"process":{"command":["true"],"restart":{"policy":"maybe"}}}`,
		`{"process":{"command":["true"],"restart":{"maximum_attempts":-1}}}`,
		`{"process":{"command":["true"],"startup_timeout":"-1s"}}`,
		`{"static":{"root":"."},"idle":"-1s"}`,
		`{"static":{"root":"."},"cron":[{"name":"same","schedule":"* * * * *","command":["true"]},{"name":"same","schedule":"* * * * *","command":["true"]}]}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadManifest(dir); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestRejectInvalidGatewaySettings(t *testing.T) {
	for _, setting := range []string{`"scan_interval":"0s"`, `"scan_interval":"-1s"`, `"default_idle":"-1s"`, `"ports":{"start":20000,"end":70000}`, `"auth":{"timeout":"0s"}`, `"index_host":"same.localhost","admin_host":"same.localhost"`} {
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(`{"apps_dir":".",`+setting+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(p); err == nil {
			t.Fatalf("accepted %s", setting)
		}
	}
}

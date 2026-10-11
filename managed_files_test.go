package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppManagedFilesPreserveInstructionsAndShareSkill(t *testing.T) {
	root := t.TempDir()
	if err := generateManagedFiles(testConfig(root), ""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "notes")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "AGENTS.md")
	local := "# Notes\r\n\r\nUse the app's tests.\r\nNo final newline"
	if err := os.WriteFile(path, []byte(local), 0600); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(dir, "AGENTS.override.md")
	if err := os.WriteFile(override, []byte("Override instructions\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := generateAppManagedFiles(dir); err != nil {
		t.Fatal(err)
	}
	body := readTestFile(t, path)
	if !strings.HasPrefix(body, local) || !strings.Contains(body, "../AGENTS.md") {
		t.Fatalf("local instructions or platform reference missing: %q", body)
	}
	if got := readTestFile(t, override); !strings.HasPrefix(got, "Override instructions\n") || !strings.Contains(got, appGuidanceStart) {
		t.Fatalf("override did not retain instructions and receive guidance: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("app-owned file permissions changed: %o", info.Mode().Perm())
	}
	link := filepath.Join(dir, ".agents", "skills", "mini-cloud-app")
	linkInfo, err := os.Lstat(link)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("skill is not a symlink: %v", err)
	}
	shared := filepath.Join(root, ".agents", "skills", "mini-cloud-app", "SKILL.md")
	if got := readTestFile(t, filepath.Join(link, "SKILL.md")); got != readTestFile(t, shared) {
		t.Fatal("linked skill does not resolve to shared skill")
	}
	if err := generateAppManagedFiles(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	linkAfter, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) || !os.SameFile(linkInfo, linkAfter) {
		t.Fatal("matching instruction file or skill link was replaced")
	}

	// Refresh only the marked section, retaining text on both sides exactly.
	prefix, suffix := "Local before\r\n", "\r\nLocal after, no newline"
	if err := os.WriteFile(path, []byte(prefix+appGuidanceStart+"\nold platform text\n"+appGuidanceEnd+suffix), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := generateAppManagedFiles(dir); err != nil {
		t.Fatal(err)
	}
	body = readTestFile(t, path)
	if !strings.HasPrefix(body, prefix) || !strings.HasSuffix(body, suffix) || strings.Contains(body, "old platform text") {
		t.Fatalf("managed section replacement damaged local instructions: %q", body)
	}
	// A shared template update is visible through every app's link.
	cfg := testConfig(root)
	cfg.BaseDomain = "changed.test"
	if err := generateManagedFiles(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, filepath.Join(link, "references", "manifest.md")), "changed.test") {
		t.Fatal("shared updates did not reach the linked skill")
	}
}

func TestAppManagedFilesRejectMalformedSections(t *testing.T) {
	for name, body := range map[string]string{
		"missing end":   "local\n" + appGuidanceStart + "\nkeep this",
		"missing start": "keep this\n" + appGuidanceEnd,
		"reversed":      appGuidanceEnd + "\nlocal\n" + appGuidanceStart,
		"duplicate":     appGuidanceStart + appGuidanceEnd + appGuidanceStart + appGuidanceEnd,
		"inline":        "Marker documentation: " + appGuidanceStart + " / " + appGuidanceEnd + "\nKeep local instructions.",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			if err := generateAppManagedFiles(dir); err == nil {
				t.Fatal("malformed section accepted")
			}
			if got := readTestFile(t, path); got != body {
				t.Fatalf("malformed file was changed: %q", got)
			}
			if _, err := os.Readlink(filepath.Join(dir, ".agents", "skills", "mini-cloud-app")); err != nil {
				t.Fatalf("independent skill link not installed: %v", err)
			}
		})
	}
}

func TestAppManagedFilesPreserveConflictingPaths(t *testing.T) {
	for _, conflict := range []string{"AGENTS.md", "AGENTS.override.md", ".agents", ".agents/skills", ".agents/skills/mini-cloud-app"} {
		for _, kind := range []string{"file", "directory", "symlink"} {
			t.Run(conflict+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, conflict)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				outside := t.TempDir()
				switch kind {
				case "file":
					if conflict == "AGENTS.md" || conflict == "AGENTS.override.md" {
						t.Skip("regular instruction files are expected")
					}
					if err := os.WriteFile(path, []byte("local file"), 0644); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if conflict == ".agents" || conflict == ".agents/skills" {
						t.Skip("parent directories are expected")
					}
					if err := os.Mkdir(path, 0755); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(outside, path); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := generateAppManagedFiles(dir); err == nil {
					t.Fatal("conflict was not reported")
				}
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("conflicting path was replaced: %v", err)
				}
				entries, err := os.ReadDir(outside)
				if err != nil || len(entries) != 0 {
					t.Fatalf("wrote through a conflicting symlink: %v", err)
				}
			})
		}
	}
}

func TestScanManagesGuidanceWithoutChangingRuntime(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "notes")
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "public", "index.html"), []byte("notes"), 0644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{Static: &StaticConfig{Root: "public"}, Access: "public"})
	g := NewGateway(testConfig(root))
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readTestFile(t, filepath.Join(dir, "AGENTS.md")), "../AGENTS.md") {
		t.Fatal("scan did not install app guidance")
	}
	a := g.apps["notes"]
	revision := a.revision
	path := filepath.Join(dir, "AGENTS.md")
	// A damaged block is reported without discarding the valid manifest.
	if err := os.WriteFile(path, []byte(appGuidanceStart+"\nlocal text"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	if a.revision != revision || a.configError != "" || a.guidanceError == "" {
		t.Fatalf("guidance error affected runtime: %+v", g.views())
	}
	if code, body := request(g, "notes.test", "/"); code != http.StatusOK || body != "notes" {
		t.Fatalf("app unavailable after guidance failure: %d %q", code, body)
	}
	if code, body := request(g, g.cfg.AdminHost, "/"); code != http.StatusOK || !strings.Contains(body, "Agent guidance:") {
		t.Fatalf("guidance error missing from admin page: %d %q", code, body)
	}
	_, body := request(g, g.cfg.AdminHost, "/api/apps")
	var views []appView
	if err := json.Unmarshal([]byte(body), &views); err != nil || len(views) != 1 || views[0].GuidanceError == "" {
		t.Fatalf("guidance error missing from API: %s (%v)", body, err)
	}
	// Local instruction edits and repairing a removed link do not revise apps.
	if err := os.WriteFile(path, []byte("new local instructions\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".agents", "skills", "mini-cloud-app")); err != nil {
		t.Fatal(err)
	}
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	if a.revision != revision || a.guidanceError != "" {
		t.Fatalf("guidance recovery changed runtime or retained error: %+v", g.views())
	}
	// Invalid newly discovered manifests still get troubleshooting guidance.
	invalid := filepath.Join(root, "invalid")
	if err := os.Mkdir(invalid, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, manifestName), []byte("{broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	if g.apps["invalid"].configError == "" || !strings.Contains(readTestFile(t, filepath.Join(invalid, "AGENTS.md")), appGuidanceStart) {
		t.Fatal("invalid app did not receive guidance and manifest error")
	}
}

func TestGenerateManagedFiles(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.BaseDomain = "apps.example.test"
	cfg.IndexHost = "apps.example.test"
	cfg.AdminHost = "admin.apps.example.test"

	if err := generateManagedFiles(cfg, "/srv/src/mini-cloud"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"AGENTS.md",
		".agents/skills/mini-cloud-app/SKILL.md",
		".agents/skills/mini-cloud-app/agents/openai.yaml",
		".agents/skills/mini-cloud-app/references/manifest.md",
	}
	for _, name := range want {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode().Perm() != 0644 {
			t.Errorf("%s mode=%o", name, info.Mode().Perm())
		}
	}

	agents := readTestFile(t, filepath.Join(root, "AGENTS.md"))
	if !strings.Contains(agents, root) || !strings.Contains(agents, "apps.example.test") {
		t.Fatalf("AGENTS.md does not contain rendered configuration:\n%s", agents)
	}
	reference := readTestFile(t, filepath.Join(root, ".agents/skills/mini-cloud-app/references/manifest.md"))
	if !strings.Contains(reference, "https://admin.apps.example.test") {
		t.Fatalf("manifest reference does not contain admin host:\n%s", reference)
	}
	skill := readTestFile(t, filepath.Join(root, ".agents/skills/mini-cloud-app/SKILL.md"))
	if !strings.Contains(skill, "`/srv/src/mini-cloud`") || strings.Contains(skill, sourceRepository) {
		t.Fatalf("skill does not point to local source:\n%s", skill)
	}
}

func TestGenerateManagedFilesUsesRepositoryWithoutSourceDir(t *testing.T) {
	root := t.TempDir()
	if err := generateManagedFiles(testConfig(root), ""); err != nil {
		t.Fatal(err)
	}
	skill := readTestFile(t, filepath.Join(root, ".agents/skills/mini-cloud-app/SKILL.md"))
	if !strings.Contains(skill, sourceRepository) {
		t.Fatalf("skill does not point to source repository:\n%s", skill)
	}
}

func TestGenerateManagedFilesReplacesChangesAndLeavesMatchingFilesAlone(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	if err := generateManagedFiles(cfg, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "AGENTS.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mtime := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	if err := generateManagedFiles(cfg, ""); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(mtime) {
		t.Fatal("matching generated file was rewritten")
	}
	if err := os.WriteFile(path, []byte("local edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := generateManagedFiles(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readTestFile(t, path), "local edit") {
		t.Fatal("local edit was not replaced")
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

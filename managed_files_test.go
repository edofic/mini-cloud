package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

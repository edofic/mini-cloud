package main

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

const sourceRepository = "https://github.com/edofic/mini-cloud"

//go:embed templates/apps-root
var managedFileTemplates embed.FS

type managedTemplateData struct {
	AppsDir    string
	BaseDomain string
	IndexHost  string
	AdminHost  string
	SourceDir  string
}

var managedFiles = map[string]string{
	"AGENTS.md":                                            "AGENTS.md.tmpl",
	".agents/skills/mini-cloud-app/SKILL.md":               "skill/SKILL.md.tmpl",
	".agents/skills/mini-cloud-app/agents/openai.yaml":     "skill/agents/openai.yaml.tmpl",
	".agents/skills/mini-cloud-app/references/manifest.md": "skill/references/manifest.md.tmpl",
}

func generateManagedFiles(cfg Config, sourceDir string) error {
	data := managedTemplateData{
		AppsDir:    cfg.AppsDir,
		BaseDomain: cfg.BaseDomain,
		IndexHost:  cfg.IndexHost,
		AdminHost:  cfg.AdminHost,
		SourceDir:  sourceDir,
	}
	for destination, source := range managedFiles {
		body, err := fs.ReadFile(managedFileTemplates, filepath.Join("templates/apps-root", source))
		if err != nil {
			return fmt.Errorf("read managed template %s: %w", source, err)
		}
		tmpl, err := template.New(source).Option("missingkey=error").Parse(string(body))
		if err != nil {
			return fmt.Errorf("parse managed template %s: %w", source, err)
		}
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, data); err != nil {
			return fmt.Errorf("render managed template %s: %w", source, err)
		}
		if err := writeManagedFile(filepath.Join(cfg.AppsDir, destination), rendered.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

func localSourceDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil || !strings.HasPrefix(string(body), "module mini-cloud\n") {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, "templates", "apps-root")); err != nil {
		return ""
	}
	return dir
}

func writeManagedFile(path string, body []byte) error {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, body) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create managed file directory for %s: %w", path, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mini-cloud-managed-*")
	if err != nil {
		return fmt.Errorf("create temporary managed file for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write managed file %s: %w", path, err)
	}
	if err := temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set managed file mode for %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close managed file %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install managed file %s: %w", path, err)
	}
	return nil
}

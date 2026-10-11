package main

import (
	"bytes"
	"embed"
	"errors"
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
	return writeFileIfChanged(path, body, 0644)
}

func writeFileIfChanged(path string, body []byte, mode fs.FileMode) error {
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
	if err := temporary.Chmod(mode); err != nil {
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

const (
	appGuidanceStart = "<!-- mini-cloud:begin -->"
	appGuidanceEnd   = "<!-- mini-cloud:end -->"
	appSkillTarget   = "../../../.agents/skills/mini-cloud-app"
)

//go:embed templates/apps-root/app-AGENTS.md.tmpl
var appGuidanceTemplate string

// App-owned text stays byte-for-byte intact; only the delimited block is managed.
// Refuse ambiguous markers rather than risking deletion of local instructions.
func mergeAppGuidance(current []byte) ([]byte, error) {
	block := []byte(strings.TrimSuffix(appGuidanceTemplate, "\n"))
	start := bytes.Index(current, []byte(appGuidanceStart))
	end := bytes.Index(current, []byte(appGuidanceEnd))
	if start == -1 && end == -1 {
		body := bytes.Clone(current)
		if len(body) != 0 {
			if body[len(body)-1] != '\n' {
				body = append(body, '\n')
			}
			body = append(body, '\n')
		}
		body = append(body, block...)
		return append(body, '\n'), nil
	}
	markerLine := func(index int, marker string) bool {
		after := index + len(marker)
		return (index == 0 || current[index-1] == '\n') &&
			(after == len(current) || current[after] == '\n' || bytes.HasPrefix(current[after:], []byte("\r\n")))
	}
	if start == -1 || end < start || bytes.Count(current, []byte(appGuidanceStart)) != 1 || bytes.Count(current, []byte(appGuidanceEnd)) != 1 || !markerLine(start, appGuidanceStart) || !markerLine(end, appGuidanceEnd) {
		return nil, fmt.Errorf("malformed mini-cloud managed section; restore one matching begin/end marker pair on separate lines")
	}
	body := bytes.Clone(current[:start])
	body = append(body, block...)
	return append(body, current[end+len(appGuidanceEnd):]...), nil
}

func updateAppGuidance(path string) error {
	mode := fs.FileMode(0644)
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var current []byte
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file; leaving it unchanged", path)
		}
		mode = info.Mode().Perm()
		current, err = os.ReadFile(path)
		if err != nil {
			return err
		}
	}
	body, err := mergeAppGuidance(current)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return writeFileIfChanged(path, body, mode)
}

func linkAppSkill(dir string) error {
	// Do not follow app-owned directory links while installing platform guidance.
	for _, name := range []string{".agents", ".agents/skills"} {
		path := filepath.Join(dir, name)
		if err := os.Mkdir(path, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s must be a directory, not a file or symlink; leaving it unchanged", path)
		}
	}
	path := filepath.Join(dir, ".agents", "skills", "mini-cloud-app")
	if target, err := os.Readlink(path); err == nil && target == appSkillTarget {
		return nil
	}
	if err := os.Symlink(appSkillTarget, path); err != nil {
		return fmt.Errorf("install skill link %s (expected target %s; existing paths are preserved): %w", path, appSkillTarget, err)
	}
	return nil
}

func generateAppManagedFiles(dir string) error {
	errs := []error{updateAppGuidance(filepath.Join(dir, "AGENTS.md")), linkAppSkill(dir)}
	// Codex prefers an override to AGENTS.md. Keep existing overrides usable too,
	// without creating one or replacing the application's chosen instructions.
	path := filepath.Join(dir, "AGENTS.override.md")
	if _, err := os.Lstat(path); err == nil {
		errs = append(errs, updateAppGuidance(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

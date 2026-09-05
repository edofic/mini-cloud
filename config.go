package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const manifestName = "mini-cloud.json"

type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	Listen       string     `json:"listen"`
	AppsDir      string     `json:"apps_dir"`
	BaseDomain   string     `json:"base_domain"`
	IndexHost    string     `json:"index_host"`
	AdminHost    string     `json:"admin_host"`
	ScanInterval Duration   `json:"scan_interval"`
	DefaultIdle  Duration   `json:"default_idle"`
	Ports        PortRange  `json:"ports"`
	Auth         AuthConfig `json:"auth"`
}

type PortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type AuthConfig struct {
	Timeout        Duration `json:"timeout"`
	IdentityHeader string   `json:"identity_header"`
	GroupsHeader   string   `json:"groups_header"`
	AdminIdentity  string   `json:"admin_identity"`
	VerifyURL      string   `json:"verify_url"`
	CopyHeaders    []string `json:"copy_headers"`
}

func loadConfig(path string) (Config, error) {
	c := Config{Listen: "127.0.0.1:9080", BaseDomain: "apps.localhost", IndexHost: "apps.localhost", AdminHost: "admin.apps.localhost", ScanInterval: Duration{time.Second}, DefaultIdle: Duration{5 * time.Minute}, Ports: PortRange{20000, 29999}, Auth: AuthConfig{Timeout: Duration{5 * time.Second}, IdentityHeader: "Remote-User", GroupsHeader: "Remote-Groups", CopyHeaders: []string{"Remote-User", "Remote-Groups", "Remote-Email", "Remote-Name"}}}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse gateway config: %w", err)
	}
	if c.AppsDir == "" {
		return c, fmt.Errorf("apps_dir is required")
	}
	c.AppsDir, err = filepath.Abs(c.AppsDir)
	if err != nil {
		return c, err
	}
	if c.Ports.Start < 1024 || c.Ports.End < c.Ports.Start || c.Ports.End > 65535 {
		return c, fmt.Errorf("invalid port range")
	}
	if c.ScanInterval.Duration <= 0 || c.DefaultIdle.Duration < 0 || c.Auth.Timeout.Duration <= 0 {
		return c, fmt.Errorf("scan_interval and auth.timeout must be positive; default_idle must be nonnegative")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("invalid listen address: %w", err)
	}
	for _, host := range []string{c.BaseDomain, c.IndexHost, c.AdminHost} {
		if !validHost(host) {
			return c, fmt.Errorf("invalid hostname %q", host)
		}
	}
	if strings.EqualFold(c.IndexHost, c.AdminHost) {
		return c, fmt.Errorf("index_host and admin_host must differ")
	}
	if c.Auth.VerifyURL != "" {
		u, err := url.Parse(c.Auth.VerifyURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("auth.verify_url must be an HTTP(S) URL")
		}
	}
	return c, nil
}

type Manifest struct {
	Host         string         `json:"host"`
	DisplayName  string         `json:"display_name"`
	Description  string         `json:"description"`
	Icon         string         `json:"icon"`
	Access       string         `json:"access"`
	AccessUsers  []string       `json:"access_users,omitempty"`
	AccessGroups []string       `json:"access_groups,omitempty"`
	Sandbox      string         `json:"sandbox,omitempty"`
	Process      *ProcessConfig `json:"process,omitempty"`
	Static       *StaticConfig  `json:"static,omitempty"`
	CGI          *CGIConfig     `json:"cgi,omitempty"`
	Idle         *Duration      `json:"idle,omitempty"`
	Watch        WatchConfig    `json:"watch"`
	Cron         []CronConfig   `json:"cron"`
}

type ProcessConfig struct {
	Command          []string          `json:"command"`
	WorkingDirectory string            `json:"working_directory"`
	Environment      map[string]string `json:"environment"`
	EnvironmentFiles []string          `json:"environment_files"`
	StartupTimeout   Duration          `json:"startup_timeout"`
	ShutdownTimeout  Duration          `json:"shutdown_timeout"`
	Readiness        ReadinessConfig   `json:"readiness"`
	Restart          RestartConfig     `json:"restart"`
}
type ReadinessConfig struct {
	Type string `json:"type"`
	Path string `json:"path"`
}
type RestartConfig struct {
	Policy          string   `json:"policy"`
	Delay           Duration `json:"delay"`
	MaximumAttempts int      `json:"maximum_attempts"`
}
type StaticConfig struct {
	Root        string `json:"root"`
	SPAFallback string `json:"spa_fallback"`
	Browse      bool   `json:"browse"`
}
type CGIConfig struct {
	Command            []string          `json:"command"`
	WorkingDirectory   string            `json:"working_directory"`
	Environment        map[string]string `json:"environment"`
	EnvironmentFiles   []string          `json:"environment_files"`
	Timeout            Duration          `json:"timeout"`
	MaximumConcurrency int               `json:"maximum_concurrency"`
}
type WatchConfig struct {
	Ignore []string `json:"ignore"`
}
type CronConfig struct {
	Name        string            `json:"name"`
	Schedule    string            `json:"schedule"`
	Command     []string          `json:"command"`
	Timeout     Duration          `json:"timeout"`
	Overlap     string            `json:"overlap"`
	Missed      string            `json:"missed"`
	Environment map[string]string `json:"environment"`
}

func loadManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Host != "" && !validHost(m.Host) {
		return m, fmt.Errorf("invalid host %q", m.Host)
	}
	if m.Idle != nil && m.Idle.Duration < 0 {
		return m, fmt.Errorf("idle must be nonnegative")
	}
	kinds := 0
	if m.Process != nil {
		kinds++
	}
	if m.Static != nil {
		kinds++
	}
	if m.CGI != nil {
		kinds++
	}
	if kinds != 1 {
		return m, fmt.Errorf("exactly one of process, static, or cgi is required")
	}
	if m.Access == "" {
		m.Access = "authenticated"
	}
	if m.Access != "public" && m.Access != "authenticated" {
		return m, fmt.Errorf("access must be public or authenticated")
	}
	if m.Access == "public" && (len(m.AccessUsers) != 0 || len(m.AccessGroups) != 0) {
		return m, fmt.Errorf("access_users and access_groups require authenticated access")
	}
	if m.Sandbox != "" && m.Sandbox != "bubblewrap" {
		return m, fmt.Errorf("sandbox must be bubblewrap or omitted")
	}
	if m.AccessUsers, err = normalizePrincipals("access_users", m.AccessUsers); err != nil {
		return m, err
	}
	if m.AccessGroups, err = normalizePrincipals("access_groups", m.AccessGroups); err != nil {
		return m, err
	}
	if m.Process != nil {
		if len(m.Process.Command) == 0 || m.Process.Command[0] == "" {
			return m, fmt.Errorf("process.command is required")
		}
		if m.Process.WorkingDirectory == "" {
			m.Process.WorkingDirectory = "."
		}
		if m.Process.StartupTimeout.Duration == 0 {
			m.Process.StartupTimeout.Duration = 30 * time.Second
		}
		if m.Process.ShutdownTimeout.Duration == 0 {
			m.Process.ShutdownTimeout.Duration = 10 * time.Second
		}
		if m.Process.Readiness.Type == "" {
			m.Process.Readiness.Type = "tcp"
		}
		if m.Process.Restart.Policy == "" {
			m.Process.Restart.Policy = "on-failure"
		}
		if m.Process.Restart.Delay.Duration == 0 {
			m.Process.Restart.Delay.Duration = 2 * time.Second
		}
		if m.Process.Restart.MaximumAttempts == 0 {
			m.Process.Restart.MaximumAttempts = 5
		}
	}
	if p := m.Process; p != nil {
		if p.StartupTimeout.Duration < 0 || p.ShutdownTimeout.Duration < 0 || p.Restart.Delay.Duration < 0 || p.Restart.MaximumAttempts < 0 {
			return m, fmt.Errorf("process timeouts, restart delay and maximum_attempts must be nonnegative")
		}
		if p.Readiness.Type != "tcp" && p.Readiness.Type != "http" {
			return m, fmt.Errorf("readiness.type must be tcp or http")
		}
		if p.Restart.Policy != "never" && p.Restart.Policy != "on-failure" && p.Restart.Policy != "always" {
			return m, fmt.Errorf("restart.policy must be never, on-failure, or always")
		}
	}
	if m.CGI != nil {
		if m.CGI.Timeout.Duration < 0 || m.CGI.MaximumConcurrency < 0 {
			return m, fmt.Errorf("CGI timeout and maximum_concurrency must be nonnegative")
		}
		if len(m.CGI.Command) == 0 || m.CGI.Command[0] == "" {
			return m, fmt.Errorf("cgi.command is required")
		}
		if m.CGI.WorkingDirectory == "" {
			m.CGI.WorkingDirectory = "."
		}
		if m.CGI.Timeout.Duration == 0 {
			m.CGI.Timeout.Duration = 30 * time.Second
		}
		if m.CGI.MaximumConcurrency == 0 {
			m.CGI.MaximumConcurrency = 4
		}
	}
	seenJobs := map[string]bool{}
	for i := range m.Cron {
		if seenJobs[m.Cron[i].Name] {
			return m, fmt.Errorf("duplicate cron name %q", m.Cron[i].Name)
		}
		seenJobs[m.Cron[i].Name] = true
		if m.Cron[i].Timeout.Duration < 0 {
			return m, fmt.Errorf("cron timeout must be nonnegative")
		}
		if m.Cron[i].Name == "" || len(m.Cron[i].Command) == 0 || m.Cron[i].Command[0] == "" {
			return m, fmt.Errorf("cron[%d] requires name and command", i)
		}
		if _, err := parseCron(m.Cron[i].Schedule); err != nil {
			return m, fmt.Errorf("cron %q: %w", m.Cron[i].Name, err)
		}
		if m.Cron[i].Overlap == "" {
			m.Cron[i].Overlap = "forbid"
		}
		if m.Cron[i].Overlap != "allow" && m.Cron[i].Overlap != "forbid" && m.Cron[i].Overlap != "replace" {
			return m, fmt.Errorf("cron %q: overlap must be allow, forbid, or replace", m.Cron[i].Name)
		}
		if m.Cron[i].Missed == "" {
			m.Cron[i].Missed = "skip"
		}
		if m.Cron[i].Missed != "skip" {
			return m, fmt.Errorf("cron %q: only missed=skip is supported", m.Cron[i].Name)
		}
	}
	return m, nil
}

func normalizePrincipals(field string, values []string) ([]string, error) {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s entries must not be empty", field)
		}
		if strings.Contains(value, ",") {
			return nil, fmt.Errorf("%s entries must not contain commas", field)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			valid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
			if !valid {
				return false
			}
		}
	}
	return true
}

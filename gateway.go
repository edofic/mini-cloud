package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Gateway struct {
	cfg        Config
	mu         sync.RWMutex
	apps       map[string]*App
	hosts      map[string]*App
	ports      map[int]string
	ctx        context.Context
	childMu    sync.Mutex
	children   map[*exec.Cmd]func() bool
	childrenWG sync.WaitGroup
	closing    bool
}

type App struct {
	gw               *Gateway
	name, dir        string
	mu               sync.Mutex
	lifecycleMu      sync.Mutex
	startCancel      context.CancelFunc
	revision         uint64
	attempts         int
	idleGeneration   uint64
	manifest         Manifest
	snapshot         string
	configError      string
	state            string
	stateError       string
	port             int
	cmd              *exec.Cmd
	active           int
	draining         bool
	restartScheduled bool
	wake             chan struct{}
	lastActivity     time.Time
	idleTimer        *time.Timer
	restarts         int
	manualStop       bool
	cron             map[string]*cronRun
	cgiSem           chan struct{}
}
type cronRun struct {
	running    map[*jobInvocation]struct{}
	config     CronConfig
	last       time.Time
	lastResult string
}
type jobInvocation struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func NewGateway(cfg Config) *Gateway {
	return &Gateway{cfg: cfg, apps: map[string]*App{}, hosts: map[string]*App{}, ports: map[int]string{}, children: map[*exec.Cmd]func() bool{}}
}

func (g *Gateway) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g.ctx = ctx
	if err := g.scan(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", g.cfg.Listen)
	if err != nil {
		return err
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); g.watch(ctx) }()
	go func() { defer workers.Done(); g.schedule(ctx) }()
	server := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		// Stop accepting requests immediately; cancellation also closes upgraded proxies.
		httpDone := make(chan struct{})
		go func() {
			defer close(httpDone)
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
		}()
		workers.Wait()
		g.stopAll()
		<-httpDone
	}()
	log.Printf("mini-cloud listening on %s; watching %s", g.cfg.Listen, g.cfg.AppsDir)
	err = server.Serve(listener)
	cancel()
	<-stopped
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	if host == g.cfg.IndexHost {
		g.serveIndex(w, r)
		return
	}
	if host == g.cfg.AdminHost {
		g.serveAdmin(w, r)
		return
	}
	g.mu.RLock()
	a := g.hosts[host]
	g.mu.RUnlock()
	if a == nil {
		http.Error(w, "unknown mini-cloud application", http.StatusNotFound)
		return
	}
	a.mu.Lock()
	manifest := a.manifest
	a.mu.Unlock()
	if manifest.Access != "public" && !g.authorizeApp(w, r, manifest) {
		return
	}
	a.serveManifest(w, r, manifest)
}

func stripPort(s string) string {
	if h, _, e := net.SplitHostPort(s); e == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(s)
}

func (g *Gateway) scan() error {
	entries, err := os.ReadDir(g.cfg.AppsDir)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(g.cfg.AppsDir, name)
		if _, err := os.Stat(filepath.Join(dir, manifestName)); err != nil {
			continue
		}
		seen[name] = true
		m, merr := loadManifest(dir)
		snap, serr := snapshotDir(dir, m.Watch.Ignore)
		if serr != nil && merr == nil {
			merr = serr
		}
		g.mu.Lock()
		a := g.apps[name]
		if a == nil {
			a = &App{gw: g, name: name, dir: dir, state: "stopped", lastActivity: time.Now(), cron: map[string]*cronRun{}}
			g.apps[name] = a
		}
		g.mu.Unlock()
		if merr != nil {
			a.setConfigError(merr)
			continue
		}
		a.mu.Lock()
		changed := a.snapshot != snap
		a.mu.Unlock()
		if changed {
			if err := g.apply(a, m, snap); err != nil {
				a.setConfigError(err)
			}
		}
	}
	g.mu.Lock()
	for n, a := range g.apps {
		if !seen[n] {
			delete(g.apps, n)
			for h, x := range g.hosts {
				if x == a {
					delete(g.hosts, h)
				}
			}
			a.mu.Lock()
			for _, job := range a.cron {
				for invocation := range job.running {
					invocation.cancel()
				}
			}
			a.manualStop = true
			a.mu.Unlock()
			go a.stop("manifest removed")
		}
	}
	g.mu.Unlock()
	return nil
}

func (g *Gateway) apply(a *App, m Manifest, snap string) error {
	host := m.Host
	if host == "" {
		host = a.name + "." + g.cfg.BaseDomain
	}
	host = strings.ToLower(host)
	g.mu.Lock()
	defer g.mu.Unlock()
	if host == g.cfg.AdminHost || host == g.cfg.IndexHost {
		return fmt.Errorf("host %s is reserved by the gateway", host)
	}
	if other := g.hosts[host]; other != nil && other != a {
		return fmt.Errorf("host %s is already used by %s", host, other.name)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.port == 0 && m.Process != nil {
		port := g.allocatePort(a.name)
		if port == 0 {
			return fmt.Errorf("no available backend ports")
		}
		a.port = port
	}
	for h, x := range g.hosts {
		if x == a && h != host {
			delete(g.hosts, h)
		}
	}
	g.hosts[host] = a
	hadConfig := a.snapshot != ""
	wasLive := a.state == "running" || a.state == "starting" || a.state == "stopping"
	a.manifest = m
	a.snapshot = snap
	a.revision++
	a.attempts = 0
	a.configError = ""
	a.syncCronLocked(m.Cron)
	if m.CGI != nil && (a.cgiSem == nil || cap(a.cgiSem) != m.CGI.MaximumConcurrency) {
		a.cgiSem = make(chan struct{}, m.CGI.MaximumConcurrency)
	}
	if hadConfig && wasLive {
		a.draining = true
		a.signalLocked()
		if a.active == 0 {
			a.scheduleRestartLocked()
		}
	}
	return nil
}

// allocatePort is called with g.mu held. External listeners are never adopted.
func (g *Gateway) allocatePort(name string) int {
	size := g.cfg.Ports.End - g.cfg.Ports.Start + 1
	var hash uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		hash ^= uint32(name[i])
		hash *= 16777619
	}
	start := int(hash % uint32(size))
	for i := 0; i < size; i++ {
		port := g.cfg.Ports.Start + (start+i)%size
		if _, reserved := g.ports[port]; reserved {
			continue
		}
		listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		g.ports[port] = name
		return port
	}
	return 0
}

func (a *App) setConfigError(err error) {
	a.mu.Lock()
	a.configError = err.Error()
	a.mu.Unlock()
	log.Printf("app=%s config_error=%q", a.name, err)
}

func (g *Gateway) watch(ctx context.Context) {
	t := time.NewTicker(g.cfg.ScanInterval.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := g.scan(); err != nil {
				log.Printf("scan error: %v", err)
			}
		}
	}
}

func snapshotDir(root string, ignores []string) (string, error) {
	h := sha256.New()
	defaults := []string{".git", ".hg", ".svn"}
	ignores = append(defaults, ignores...)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		for _, p := range ignores {
			if ok, _ := filepath.Match(p, rel); ok || strings.HasPrefix(rel, p+string(os.PathSeparator)) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", rel, info.Size(), info.ModTime().UnixNano(), info.Mode())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *App) signalLocked() {
	if a.wake != nil {
		close(a.wake)
	}
	a.wake = make(chan struct{})
}
func (a *App) waitChannelLocked() <-chan struct{} {
	if a.wake == nil {
		a.wake = make(chan struct{})
	}
	return a.wake
}

func (a *App) serveManifest(w http.ResponseWriter, r *http.Request, m Manifest) {
	switch {
	case m.Static != nil:
		a.serveStatic(w, r, m.Static)
	case m.CGI != nil:
		a.serveCGI(w, r, m.CGI)
	default:
		a.serveProcess(w, r)
	}
}

func (a *App) serveProcess(w http.ResponseWriter, r *http.Request) {
	port, release, err := a.acquire(r.Context())
	if err != nil {
		http.Error(w, "application unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer release()
	a.proxy(w, r, "127.0.0.1:"+strconv.Itoa(port))
}

func (a *App) acquire(ctx context.Context) (int, func(), error) {
	attempted := false
	for {
		a.mu.Lock()
		if a.manualStop || a.gw.ctx.Err() != nil {
			a.mu.Unlock()
			return 0, nil, fmt.Errorf("application stopped")
		}
		if a.manifest.Process == nil {
			a.mu.Unlock()
			return 0, nil, fmt.Errorf("application runtime changed; retry request")
		}
		switch a.state {
		case "running":
			if !a.draining {
				a.active++
				a.idleGeneration++
				if a.idleTimer != nil {
					a.idleTimer.Stop()
				}
				p := a.port
				a.mu.Unlock()
				return p, a.release, nil
			}
		case "stopped", "failed":
			if attempted {
				err := a.stateError
				if err == "" {
					err = "process exited before readiness"
				}
				a.mu.Unlock()
				return 0, nil, fmt.Errorf("%s", err)
			}
			if !a.draining && !a.manualStop {
				attempted = true
				a.state = "starting"
				a.stateError = ""
				a.attempts = 0
				go a.start()
			}
		}
		ch := a.waitChannelLocked()
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-ch:
		}
	}
}
func (a *App) release() {
	a.mu.Lock()
	if a.active > 0 {
		a.active--
	}
	a.lastActivity = time.Now()
	drain := a.draining && a.active == 0
	if !a.draining {
		a.armIdleLocked()
	}
	a.mu.Unlock()
	if drain {
		a.mu.Lock()
		a.scheduleRestartLocked()
		a.mu.Unlock()
	}
}

func (a *App) scheduleRestartLocked() {
	if a.restartScheduled {
		return
	}
	a.restartScheduled = true
	go a.restartAfterDrain()
}
func (a *App) idleDurationLocked() time.Duration {
	if a.manifest.Idle != nil {
		return a.manifest.Idle.Duration
	}
	return a.gw.cfg.DefaultIdle.Duration
}
func (a *App) armIdleLocked() {
	a.idleGeneration++
	if a.idleTimer != nil {
		a.idleTimer.Stop()
	}
	d := a.idleDurationLocked()
	if d <= 0 || a.active != 0 || a.manualStop {
		return
	}
	generation := a.idleGeneration
	a.idleTimer = time.AfterFunc(d, func() {
		a.mu.Lock()
		idle := generation == a.idleGeneration && a.state == "running" && a.active == 0 && !a.draining && !a.manualStop
		if idle {
			a.state = "stopping"
			a.signalLocked()
		}
		a.mu.Unlock()
		if idle {
			a.stop("idle timeout")
		}
	})
}

func (a *App) start() {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.mu.Lock()
	if a.state != "starting" || a.manualStop || a.manifest.Process == nil || a.gw.ctx.Err() != nil {
		a.signalLocked()
		a.mu.Unlock()
		return
	}
	pc := *a.manifest.Process
	port := a.port
	ctx, cancel := context.WithTimeout(a.gw.ctx, pc.StartupTimeout.Duration)
	a.startCancel = cancel
	a.mu.Unlock()
	defer func() { cancel(); a.mu.Lock(); a.startCancel = nil; a.mu.Unlock() }()
	// A listener may have occupied this stable port since the last activation.
	probe, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		a.startFailed(fmt.Errorf("backend port %d is occupied: %w", port, err))
		return
	}
	_ = probe.Close()
	env, err := buildEnv(a.dir, pc.WorkingDirectory, pc.EnvironmentFiles, pc.Environment, map[string]string{"PORT": strconv.Itoa(port), "LISTEN_ADDRESS": "127.0.0.1:" + strconv.Itoa(port), "APP_NAME": a.name})
	if err != nil {
		a.startFailed(err)
		return
	}
	cmd := appCommand(a.dir, pc.WorkingDirectory, interpolate(pc.Command, env))
	cmd.Env = envList(env)
	cmd.Stdout = &logWriter{prefix: "app=" + a.name + " stream=stdout "}
	cmd.Stderr = &logWriter{prefix: "app=" + a.name + " stream=stderr "}
	a.mu.Lock()
	if a.state != "starting" || a.manualStop || ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	// Process apps receive graceful shutdown through stop; auxiliary children use
	// their request/job context directly.
	if err = a.gw.startChild(context.Background(), cmd); err != nil {
		a.mu.Unlock()
		a.startFailed(err)
		return
	}
	a.cmd = cmd
	a.mu.Unlock()
	go a.waitProcess(cmd, pc)
	err = waitReady(ctx, port, pc.Readiness)
	a.mu.Lock()
	if a.cmd != cmd || a.state != "starting" {
		a.mu.Unlock()
		return
	}
	if err != nil {
		a.state = "stopping"
		a.stateError = err.Error()
		a.mu.Unlock()
		a.kill(cmd, pc.ShutdownTimeout.Duration)
		a.mu.Lock()
		if !a.manualStop {
			a.state = "failed"
		}
		a.signalLocked()
		a.mu.Unlock()
		return
	}
	a.state = "running"
	a.lastActivity = time.Now()
	a.signalLocked()
	a.armIdleLocked()
	a.mu.Unlock()
	log.Printf("app=%s event=ready port=%d", a.name, port)
}

func (a *App) startFailed(err error) {
	a.mu.Lock()
	if a.state == "starting" {
		a.state = "failed"
		a.stateError = err.Error()
	}
	a.signalLocked()
	a.mu.Unlock()
	log.Printf("app=%s event=start_failed error=%q", a.name, err)
}

func (a *App) waitProcess(cmd *exec.Cmd, pc ProcessConfig) {
	err := a.gw.waitChild(cmd)
	a.mu.Lock()
	if a.cmd != cmd {
		a.mu.Unlock()
		return
	}
	a.cmd = nil
	wasStarting := a.state == "starting"
	requested := a.state == "stopping" || a.draining || a.manualStop || a.gw.ctx.Err() != nil
	a.state = "stopped"
	failed := err != nil || wasStarting
	if !requested && failed {
		a.state = "failed"
		if err != nil {
			a.stateError = err.Error()
		} else {
			a.stateError = "process exited before readiness"
		}
	}
	a.signalLocked()
	revision := a.revision
	retry := !requested && failed && pc.Restart.Policy == "on-failure" && a.attempts < pc.Restart.MaximumAttempts
	if retry {
		a.attempts++
		a.restarts++
	}
	a.mu.Unlock()
	log.Printf("app=%s event=exit error=%v", a.name, err)
	if retry {
		timer := time.NewTimer(pc.Restart.Delay.Duration)
		defer timer.Stop()
		select {
		case <-a.gw.ctx.Done():
			return
		case <-timer.C:
		}
		a.mu.Lock()
		if a.state == "failed" && !a.manualStop && !a.draining && a.revision == revision && a.manifest.Process != nil {
			a.state = "starting"
			go a.start()
		}
		a.mu.Unlock()
	}
}

func (a *App) stop(reason string) {
	a.mu.Lock()
	a.state = "stopping"
	a.idleGeneration++
	if a.idleTimer != nil {
		a.idleTimer.Stop()
	}
	if a.startCancel != nil {
		a.startCancel()
	}
	a.signalLocked()
	a.mu.Unlock()
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	a.mu.Lock()
	cmd := a.cmd
	timeout := 10 * time.Second
	if a.manifest.Process != nil {
		timeout = a.manifest.Process.ShutdownTimeout.Duration
	}
	a.mu.Unlock()
	if cmd != nil {
		log.Printf("app=%s event=stop reason=%q", a.name, reason)
		a.kill(cmd, timeout)
	}
	a.mu.Lock()
	a.state = "stopped"
	a.signalLocked()
	a.mu.Unlock()
}

func (a *App) kill(cmd *exec.Cmd, timeout time.Duration) {
	a.gw.signalChild(cmd, syscall.SIGTERM)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.mu.Lock()
		done := a.cmd != cmd
		a.mu.Unlock()
		if done {
			return
		}
		select {
		case <-timer.C:
			a.gw.signalChild(cmd, syscall.SIGKILL)
			// waitChild bounds inherited pipe waits; wait for it to publish completion.
			timer.Reset(time.Second)
		case <-ticker.C:
		}
	}
}

func (a *App) restartAfterDrain() {
	a.stop("files changed")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.draining = false
	a.restartScheduled = false
	if a.manualStop || a.gw.ctx.Err() != nil || a.manifest.Process == nil {
		a.state = "stopped"
		a.signalLocked()
		return
	}
	a.state = "starting"
	a.restarts++
	a.signalLocked()
	go a.start()
}

func waitReady(ctx context.Context, port int, r ReadinessConfig) error {
	client := http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		var err error
		if r.Type == "http" {
			path := r.Path
			if path == "" {
				path = "/"
			}
			req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
			if e != nil {
				return e
			}
			resp, e := client.Do(req)
			err = e
			if e == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 400 {
					return nil
				}
				err = fmt.Errorf("readiness returned %s", resp.Status)
			}
		} else {
			var c net.Conn
			c, err = net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
			if err == nil {
				_ = c.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness timeout: %w (last error: %v)", ctx.Err(), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func buildEnv(appDir, work string, files []string, values, extra map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, x := range os.Environ() {
		if i := strings.IndexByte(x, '='); i > 0 {
			out[x[:i]] = x[i+1:]
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(resolveWithin(appDir, f))
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return nil, fmt.Errorf("invalid environment line in %s", f)
			}
			k = strings.TrimSpace(k)
			if k == "" || strings.ContainsAny(k, "\x00=") {
				return nil, fmt.Errorf("invalid environment key in %s", f)
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			out[k] = v
		}
	}
	for k, v := range values {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out, nil
}
func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
func interpolate(in []string, env map[string]string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = os.Expand(s, func(k string) string { return env[k] })
	}
	return out
}
func resolveWithin(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

const maxLogLine = 64 * 1024

type logWriter struct {
	prefix string
	buf    []byte
	mu     sync.Mutex
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		room := maxLogLine - len(w.buf)
		take := min(room, len(p))
		chunk := p[:take]
		if i := strings.IndexByte(string(chunk), '\n'); i >= 0 {
			w.buf = append(w.buf, chunk[:i]...)
			log.Printf("%s%s", w.prefix, w.buf)
			w.buf = w.buf[:0]
			p = p[i+1:]
		} else {
			w.buf = append(w.buf, chunk...)
			p = p[take:]
			if len(w.buf) == maxLogLine {
				log.Printf("%s%s [line continued]", w.prefix, w.buf)
				w.buf = w.buf[:0]
			}
		}
	}
	return n, nil
}

func (g *Gateway) stopAll() {
	g.childMu.Lock()
	g.closing = true
	g.childMu.Unlock()
	g.mu.RLock()
	apps := make([]*App, 0, len(g.apps))
	for _, a := range g.apps {
		apps = append(apps, a)
	}
	g.mu.RUnlock()
	// One upper bound across apps, even when a manifest requests a long grace.
	force := time.AfterFunc(15*time.Second, g.killChildren)
	defer force.Stop()
	var wg sync.WaitGroup
	for _, a := range apps {
		a.mu.Lock()
		a.manualStop = true
		for _, job := range a.cron {
			for invocation := range job.running {
				invocation.cancel()
			}
		}
		a.mu.Unlock()
		wg.Add(1)
		go func(app *App) { defer wg.Done(); app.stop("gateway shutdown") }(a)
	}
	wg.Wait()
	g.killChildren()
	g.childrenWG.Wait()
}

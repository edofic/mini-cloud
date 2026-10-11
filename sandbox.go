package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Hardened is opt-in. The existing lightweight bubblewrap preset is unchanged.
const hardenedSandbox = "bubblewrap-hardened"

type SandboxOptions struct {
	WritablePaths []string       `json:"writable_paths,omitempty"`
	ReadOnlyPaths []string       `json:"read_only_paths,omitempty"`
	Network       SandboxNetwork `json:"network,omitempty"`
}
type SandboxNetwork struct {
	AllowHosts     []string `json:"allow_hosts,omitempty"` // Exact DNS names, or * for public Internet.
	ForwardAuth    bool     `json:"forward_auth,omitempty"`
	MaxConnections int      `json:"max_connections,omitempty"`
}
type SandboxRuntime struct {
	RuntimePaths          []string `json:"runtime_paths,omitempty"`
	RuntimePathsFile      string   `json:"runtime_paths_file,omitempty"`
	EgressConnectionLimit int      `json:"egress_connection_limit,omitempty"`
}

func validateSandboxOptions(m *Manifest) error {
	if m.SandboxOptions == nil {
		return nil
	}
	if m.Sandbox != hardenedSandbox {
		return fmt.Errorf("sandbox_options requires %s", hardenedSandbox)
	}
	for _, p := range m.SandboxOptions.WritablePaths {
		clean := filepath.Clean(p)
		if filepath.IsAbs(p) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("sandbox writable paths must be app-relative subdirectories")
		}
	}
	for _, p := range m.SandboxOptions.ReadOnlyPaths {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return fmt.Errorf("sandbox read-only paths must be absolute and cannot be /")
		}
	}
	if n := m.SandboxOptions.Network.MaxConnections; n < 0 || n > 256 {
		return fmt.Errorf("sandbox network max_connections must be 0 (default) or 1..256")
	}
	for _, host := range m.SandboxOptions.Network.AllowHosts {
		if host != "*" && (!validHost(host) || net.ParseIP(host) != nil) {
			return fmt.Errorf("sandbox network allow_hosts requires exact DNS hostnames or *")
		}
	}
	return nil
}

// Only this app's configured public aliases select its public verifier. Other
// browser hosts retain the private verifier; this is not general host routing.
func (g *Gateway) sandboxVerificationURLs(name string) map[string]string {
	urls := map[string]string{}
	if p := g.cfg.PublicIngress; p != nil && p.VerifyURL != "" {
		for host, app := range p.Hosts {
			if app == name {
				urls[host] = p.VerifyURL
			}
		}
	}
	return urls
}

func (g *Gateway) sandboxRuntime(name string, opts *SandboxOptions) (string, error) {
	g.sandboxMu.Lock()
	defer g.sandboxMu.Unlock()
	if g.sandboxRoot == "" {
		root, err := os.MkdirTemp("", "mini-cloud-sandbox-")
		if err != nil {
			return "", err
		}
		g.sandboxRoot = root
	}
	dir := filepath.Join(g.sandboxRoot, name)
	if err := os.MkdirAll(filepath.Join(dir, "backend"), 0700); err != nil {
		return "", err
	}
	if g.sandboxPolicies == nil {
		g.sandboxPolicies = map[string]*egressPolicy{}
	}
	if policy := g.sandboxPolicies[name]; policy != nil {
		policy.set(opts, g.cfg.Auth.VerifyURL, g.sandboxVerificationURLs(name))
		return dir, nil
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "egress.sock"))
	if err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(dir, "egress.sock"), 0600); err != nil {
		_ = listener.Close()
		return "", err
	}
	policy := &egressPolicy{}
	policy.set(opts, g.cfg.Auth.VerifyURL, g.sandboxVerificationURLs(name))
	g.sandboxPolicies[name] = policy
	limit := 32
	if opts != nil && opts.Network.MaxConnections > 0 {
		limit = opts.Network.MaxConnections
	}
	if g.egressSlots == nil {
		total := g.cfg.Sandbox.EgressConnectionLimit
		if total <= 0 {
			total = 128
		}
		g.egressSlots = make(chan struct{}, total)
	}
	bounded := newEgressListener(listener, limit, g.egressSlots)
	server := newEgressServer(policy)
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	context.AfterFunc(ctx, func() { _ = bounded.Close(); _ = server.Close() })
	go func() {
		if err := server.Serve(bounded); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("app=%s egress_error=%q", name, err)
		}
	}()
	return dir, nil
}

func (g *Gateway) sandboxCommand(appDir, work string, args []string, sandbox string, opts *SandboxOptions, runtimeDir string, env map[string]string) (*exec.Cmd, error) {
	if sandbox != hardenedSandbox {
		cmd := appCommand(appDir, work, args, sandbox)
		cmd.Env = envList(env)
		return cmd, nil
	}
	if runtimeDir == "" {
		return nil, fmt.Errorf("hardened sandbox runtime missing")
	}
	runtimePaths := append([]string(nil), g.cfg.Sandbox.RuntimePaths...)
	if file := g.cfg.Sandbox.RuntimePathsFile; file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Fields(string(b)) {
			if !filepath.IsAbs(p) || !strings.HasPrefix(p, "/nix/store/") {
				return nil, fmt.Errorf("runtime_paths_file must contain Nix store paths")
			}
			runtimePaths = append(runtimePaths, p)
		}
	}
	if len(runtimePaths) == 0 {
		return nil, fmt.Errorf("hardened sandbox requires gateway sandbox.runtime_paths or runtime_paths_file")
	}
	workDir := resolveWithin(appDir, work)
	path := args[0]
	if strings.ContainsRune(path, filepath.Separator) && !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	bargs := []string{"--die-with-parent", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--unshare-net", "--disable-userns", "--cap-drop", "ALL", "--new-session", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--tmpfs", "/var/tmp", "--tmpfs", "/home/mini-cloud"}
	for _, p := range runtimePaths {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return nil, fmt.Errorf("invalid runtime path %q", p)
		}
		bargs = append(bargs, "--ro-bind", p, p)
	}
	// Bind concrete trust/timezone files rather than the host /etc hierarchy.
	for _, p := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/cert.pem", "/etc/localtime"} {
		if _, err := os.Stat(p); err == nil {
			bargs = append(bargs, "--ro-bind", p, p)
		}
	}
	bargs = append(bargs, "--ro-bind", appDir, appDir)
	writable := []string{"data", "tmp"}
	if opts != nil && opts.WritablePaths != nil {
		writable = opts.WritablePaths
	}
	root, err := os.OpenRoot(appDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var mounts []*os.File
	success := false
	defer func() {
		if !success {
			for _, f := range mounts {
				_ = f.Close()
			}
		}
	}()
	for _, p := range writable {
		if err := root.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
		f, err := root.Open(p)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, f)
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("writable path must be a directory")
		}
		// OpenRoot prevents traversal and symlink escapes; the FD pins the
		// directory so replacement between validation and mount cannot escape.
		bargs = append(bargs, "--bind-fd", fmt.Sprint(2+len(mounts)), resolveWithin(appDir, p))
	}
	if opts != nil {
		for _, p := range opts.ReadOnlyPaths {
			bargs = append(bargs, "--ro-bind", p, p)
		}
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	bargs = append(bargs, "--ro-bind", self, "/run/mini-cloud/runner", "--ro-bind", filepath.Join(runtimeDir, "egress.sock"), "/run/mini-cloud/egress.sock", "--bind", filepath.Join(runtimeDir, "backend"), "/run/mini-cloud/backend", "--chdir", workDir)
	// App environment must not reach Bubblewrap's host-side dynamic loader.
	// Set it only after entering the namespace, and keep secrets out of argv.
	inside := []string{"--clearenv"}
	for _, entry := range envList(env) {
		key, value, _ := strings.Cut(entry, "=")
		inside = append(inside, "--setenv", key, value)
	}
	bargs = append(inside, bargs...)
	commandJSON, err := json.Marshal(append([]string{path}, args[1:]...))
	if err != nil {
		return nil, err
	}
	bargs = append(bargs, "--setenv", "MINI_CLOUD_SANDBOX_COMMAND", string(commandJSON))
	for _, arg := range bargs {
		if strings.ContainsRune(arg, '\x00') {
			return nil, fmt.Errorf("sandbox arguments may not contain NUL")
		}
	}
	argumentFile, err := os.CreateTemp(runtimeDir, "args-")
	if err != nil {
		return nil, err
	}
	mounts = append(mounts, argumentFile)
	if err := os.Remove(argumentFile.Name()); err != nil {
		return nil, err
	}
	if _, err := argumentFile.WriteString(strings.Join(bargs, "\x00") + "\x00"); err != nil {
		return nil, err
	}
	if _, err := argumentFile.Seek(0, 0); err != nil {
		return nil, err
	}
	cmd := exec.Command("bwrap", "--args", fmt.Sprint(2+len(mounts)), "--", "/run/mini-cloud/runner", "sandbox-exec")
	cmd.Dir = workDir
	cmd.Env = envList(hardenedEnv(appDir))
	cmd.ExtraFiles = mounts
	success = true
	return cmd, nil
}

func hardenedEnv(appDir string) map[string]string {
	return map[string]string{"PATH": "/run/current-system/sw/bin:/usr/bin:/bin", "HOME": "/home/mini-cloud", "TMPDIR": "/tmp", "LANG": "C.UTF-8", "MINI_CLOUD_SANDBOX": "hardened", "SSL_CERT_FILE": "/etc/ssl/certs/ca-certificates.crt", "GOTOOLCHAIN": "local", "GOPATH": filepath.Join(appDir, "tmp", "go"), "GOCACHE": filepath.Join(appDir, "tmp", "go-build"), "CGO_ENABLED": "0"}
}

// Runs inside the private namespace. A loopback byte relay makes conventional
// HTTP_PROXY clients (including source runners) use the host's filtered Unix proxy.
func sandboxExec(args []string) error {
	if len(args) == 0 {
		if err := json.Unmarshal([]byte(os.Getenv("MINI_CLOUD_SANDBOX_COMMAND")), &args); err != nil || len(args) == 0 {
			return fmt.Errorf("sandbox-exec requires a command")
		}
	}
	if err := os.Unsetenv("MINI_CLOUD_SANDBOX_COMMAND"); err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	proxy := "http://" + listener.Addr().String()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if err := os.Setenv(key, proxy); err != nil {
			return err
		}
	}
	for _, key := range []string{"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		if err := os.Unsetenv(key); err != nil {
			return err
		}
	}
	relaySlots := make(chan struct{}, 128)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case relaySlots <- struct{}{}:
			default:
				_ = conn.Close()
				continue
			}
			go func() {
				defer func() { <-relaySlots }()
				defer func() { _ = conn.Close() }()
				upstream, err := net.DialTimeout("unix", "/run/mini-cloud/egress.sock", 10*time.Second)
				if err != nil {
					return
				}
				defer func() { _ = upstream.Close() }()
				done := make(chan struct{})
				// Peer disconnects end the relay; cleanup releases the slot.
				go func() {
					_, _ = io.Copy(upstream, conn)
					_ = upstream.(*net.UnixConn).CloseWrite()
					close(done)
				}()
				_, _ = io.Copy(conn, upstream)
				_ = conn.Close()
				_ = upstream.Close()
				<-done
			}()
		}
	}()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

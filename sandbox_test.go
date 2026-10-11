package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		if err := sandboxExec(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "sandbox-probe" {
		codeErr := os.WriteFile("code.txt", []byte("modified"), 0600)
		dataErr := os.WriteFile("data/write.txt", []byte("allowed"), 0600)
		_, secretErr := os.ReadFile(os.Getenv("HOST_SECRET_PATH"))
		resp, egressErr := http.Get("https://api.openai.com/v1/models")
		egressDenied := egressErr != nil || resp.StatusCode == 403
		if resp != nil {
			_ = resp.Body.Close()
		}
		conn, hostErr := net.DialTimeout("tcp", os.Getenv("HOST_BACKEND"), 200*time.Millisecond)
		if conn != nil {
			_ = conn.Close()
		}
		_, utcOffset := time.Now().Zone()
		if err := json.NewEncoder(os.Stdout).Encode(map[string]bool{"code_readonly": codeErr != nil, "data_writable": dataErr == nil, "host_secret_hidden": secretErr != nil, "host_network_hidden": hostErr != nil, "host_env_hidden": os.Getenv("HOST_SECRET_ENV") == "", "egress_denied_by_default": egressDenied, "declared_env_inside_only": os.Getenv("LD_PRELOAD") == "/missing/sandbox-loader-sentinel", "host_timezone_preserved": fmt.Sprint(utcOffset) == os.Getenv("EXPECTED_UTC_OFFSET")}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "sandbox-cgi-probe" {
		fmt.Print("Content-Type: text/plain\r\n\r\n")
		fmt.Printf("%s|%s|%s", os.Getenv("REQUEST_METHOD"), os.Getenv("HTTP_REMOTE_USER"), os.Getenv("LD_TRACE_LOADED_OBJECTS"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestHardenedPublicCGIMetadataAndLauncherEnvironment(t *testing.T) {
	runtimeFile := os.Getenv("MINICLOUD_SANDBOX_TEST_RUNTIME")
	if runtimeFile == "" {
		t.Skip("set MINICLOUD_SANDBOX_TEST_RUNTIME for actual Bubblewrap CGI verification")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "cgi")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{Access: "public", Sandbox: hardenedSandbox, CGI: &CGIConfig{Command: []string{"/run/mini-cloud/runner", "sandbox-cgi-probe"}, Environment: map[string]string{"LD_TRACE_LOADED_OBJECTS": "1"}}})
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Remote-User", "verified") }))
	defer verifier.Close()
	cfg := testConfig(root)
	cfg.Sandbox.RuntimePathsFile = runtimeFile
	cfg.PublicIngress = &PublicIngressConfig{Hosts: map[string]string{"cgi.test": "cgi"}}
	cfg.Auth = AuthConfig{IdentityHeader: "Remote-User", VerifyURL: verifier.URL, CopyHeaders: []string{"Remote-User"}}
	g := NewGateway(cfg)
	ctx, cancel := context.WithCancel(t.Context())
	g.ctx = ctx
	defer func() { cancel(); g.stopAll(); _ = os.RemoveAll(g.sandboxRoot) }()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	g.servePublicHTTP(w, httptest.NewRequest("POST", "https://cgi.test/api", strings.NewReader("payload")))
	if w.Code != 200 || w.Body.String() != "POST|verified|1" {
		t.Fatalf("CGI status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestHardenedEnvironmentDoesNotInheritSecrets(t *testing.T) {
	t.Setenv("HOST_SECRET_ENV", "secret")
	env, err := buildEnv(t.TempDir(), ".", nil, map[string]string{"DECLARED": "visible"}, nil, false)
	if err != nil || env["HOST_SECRET_ENV"] != "" || env["HOME"] != "/home/mini-cloud" || env["DECLARED"] != "visible" {
		t.Fatalf("unexpected hardened environment: %v", err)
	}
}

func TestHardenedSandboxOptionsRejectEscapes(t *testing.T) {
	for _, path := range []string{".", "..", "../other", "/home"} {
		m := Manifest{Sandbox: hardenedSandbox, SandboxOptions: &SandboxOptions{WritablePaths: []string{path}}}
		if validateSandboxOptions(&m) == nil {
			t.Fatalf("accepted writable escape %q", path)
		}
	}
	m := Manifest{Sandbox: "bubblewrap", SandboxOptions: &SandboxOptions{}}
	if validateSandboxOptions(&m) == nil {
		t.Fatal("legacy sandbox cannot silently accept hardened options")
	}
}

func TestEgressBlocksPrivateAndSpecialNetworks(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "100.100.100.100", "169.254.169.254", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2001:db8::1", "2002:7f00:1::"} {
		if publicIP(net.ParseIP(ip)) {
			t.Fatalf("accepted %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Fatalf("blocked public %s", ip)
		}
	}
}

func TestEgressExactHostPolicyAndNoDirectAuthTunnel(t *testing.T) {
	p := &egressPolicy{}
	p.set(&SandboxOptions{Network: SandboxNetwork{AllowHosts: []string{"api.openai.com"}, ForwardAuth: true}}, "http://127.0.0.1:9091/api/authz/forward-auth", nil)
	if !p.allowed("api.openai.com") || p.allowed("other.openai.com") || p.allowed("api.openai.com.attacker.test") {
		t.Fatal("host allowlist is not exact")
	}
	server := newEgressServer(p)
	for _, target := range []string{"http://127.0.0.1/", "http://10.0.0.1/", "http://auth.mini-cloud.internal/other"} {
		r := httptest.NewRequest("GET", target, nil)
		w := httptest.NewRecorder()
		server.Handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("allowed %s: %d", target, w.Code)
		}
	}
	r := httptest.NewRequest("CONNECT", "http://auth.mini-cloud.internal:443", nil)
	r.Host = "auth.mini-cloud.internal:443"
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("auth alias permitted a tunnel")
	}
}

func TestHardenedSandboxIntegration(t *testing.T) {
	if os.Getenv("MINICLOUD_SANDBOX_TEST_RUNTIME") == "" {
		t.Skip("set MINICLOUD_SANDBOX_TEST_RUNTIME to a newline-delimited runtime closure")
	}
	appDir := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "code.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(filepath.Dir(appDir), "host-secret")
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("HOST_SECRET_ENV", "secret")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := NewGateway(Config{Sandbox: SandboxRuntime{RuntimePathsFile: os.Getenv("MINICLOUD_SANDBOX_TEST_RUNTIME")}})
	g.ctx = ctx
	runtimeDir, err := g.sandboxRuntime("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(g.sandboxRoot) }()
	_, utcOffset := time.Now().Zone()
	env, err := buildEnv(appDir, ".", nil, map[string]string{"EXPECTED_UTC_OFFSET": fmt.Sprint(utcOffset), "HOST_SECRET_PATH": secret, "HOST_BACKEND": listener.Addr().String(), "LD_PRELOAD": "/missing/sandbox-loader-sentinel", "DECLARED_SECRET": "secret-not-in-argv"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := g.sandboxCommand(appDir, ".", []string{"/run/mini-cloud/runner", "sandbox-probe"}, hardenedSandbox, nil, runtimeDir, env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "secret-not-in-argv") || strings.Contains(strings.Join(cmd.Env, " "), "LD_PRELOAD=") {
		t.Fatal("app secrets or loader environment reached host launcher")
	}
	for _, f := range cmd.ExtraFiles {
		defer func() { _ = f.Close() }()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox: %v %s", err, out)
	}
	var result map[string]bool
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("probe output: %v %s", err, out)
	}
	for check, ok := range result {
		if !ok {
			t.Errorf("sandbox check failed: %s", check)
		}
	}
	if len(result) != 8 {
		t.Fatal("missing probe checks")
	}
}

func TestEgressAuthAliasOnlyForwardsVerification(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/api/authz/forward-auth?policy=public" || r.Header.Get("Cookie") != "session=test" {
			t.Error("wrong auth forwarding")
		}
		for name, want := range map[string]string{
			"X-Forwarded-For":    "203.0.113.7",
			"X-Forwarded-Host":   "shopping.test",
			"X-Forwarded-Proto":  "https",
			"X-Forwarded-Method": "POST",
			"X-Forwarded-Uri":    "/api/items?done=1",
		} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("verifier header %s = %q, want %q", name, got, want)
			}
		}
		if r.Header.Get("X-Hop-Only") != "" {
			t.Error("hop-by-hop header reached verifier")
		}
		w.Header().Set("Remote-User", "verified")
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	p := &egressPolicy{}
	p.set(&SandboxOptions{Network: SandboxNetwork{ForwardAuth: true}}, upstream.URL+"/api/authz/forward-auth?policy=public", nil)
	r := httptest.NewRequest("GET", "http://auth.mini-cloud.internal/verify", nil)
	r.Header.Set("Cookie", "session=test")
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	r.Header.Set("X-Forwarded-Host", "shopping.test")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Method", "POST")
	r.Header.Set("X-Forwarded-Uri", "/api/items?done=1")
	r.Header.Set("Connection", "X-Hop-Only")
	r.Header.Set("X-Hop-Only", "remove-me")
	w := httptest.NewRecorder()
	newEgressServer(p).Handler.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Remote-User") != "verified" {
		t.Fatalf("auth verification failed: %d", w.Code)
	}
	for _, path := range []string{"/verify?target=other", "/api/configuration", "/"} {
		r := httptest.NewRequest("GET", "http://auth.mini-cloud.internal"+path, strings.NewReader(""))
		w := httptest.NewRecorder()
		newEgressServer(p).Handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("permitted extra auth surface %s", path)
		}
	}
}

func TestHardenedEnvironmentFilesStayInsideApp(t *testing.T) {
	app := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(outside, []byte("SECRET=value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(app, "link.env")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, "../secret.env", "link.env"} {
		if _, err := buildEnv(app, ".", []string{path}, nil, nil, false); err == nil {
			t.Fatalf("read environment outside app: %s", path)
		}
	}
	if err := os.WriteFile(filepath.Join(app, "local.env"), []byte("DECLARED=ok"), 0600); err != nil {
		t.Fatal(err)
	}
	env, err := buildEnv(app, ".", []string{"local.env"}, nil, nil, false)
	if err != nil || env["DECLARED"] != "ok" {
		t.Fatalf("app environment file: %v", err)
	}
}

func TestEgressAuthAliasSelectsAppScopedPublicVerifier(t *testing.T) {
	makeVerifier := func(identity string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/verify" {
				t.Error("verification escaped configured path")
			}
			w.Header().Set("Remote-User", identity)
		}))
	}
	private := makeVerifier("private")
	defer private.Close()
	public := makeVerifier("public")
	defer public.Close()
	g := NewGateway(testConfig(t.TempDir()))
	g.cfg.PublicIngress = &PublicIngressConfig{VerifyURL: public.URL + "/verify", Hosts: map[string]string{"shopping.test": "shopping", "other.test": "other"}}
	p := &egressPolicy{}
	opts := &SandboxOptions{Network: SandboxNetwork{ForwardAuth: true}}
	p.set(opts, private.URL+"/verify", g.sandboxVerificationURLs("shopping"))
	for host, want := range map[string]string{"shopping.test": "public", "SHOPPING.test:443": "public", "shopping.private.test": "private", "other.test": "private", "": "private"} {
		r := httptest.NewRequest("GET", "http://auth.mini-cloud.internal/verify", nil)
		r.Header.Set("X-Forwarded-Host", host)
		w := httptest.NewRecorder()
		newEgressServer(p).Handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Remote-User") != want {
			t.Fatalf("host %q: status %d identity %q, want %q", host, w.Code, w.Header().Get("Remote-User"), want)
		}
	}
	p.set(nil, private.URL+"/verify", g.sandboxVerificationURLs("shopping"))
	r := httptest.NewRequest("GET", "http://auth.mini-cloud.internal/verify", nil)
	r.Header.Set("X-Forwarded-Host", "shopping.test")
	w := httptest.NewRecorder()
	newEgressServer(p).Handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("disabled forward_auth retained public verifier access")
	}
}

func TestSandboxBackendDoesNotFollowSymlinks(t *testing.T) {
	if os.Getenv("MINICLOUD_SANDBOX_TEST_RUNTIME") == "" {
		t.Skip("Linux sandbox integration check")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "app.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	conn, err := dialSandboxBackend(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	link := filepath.Join(dir, "link.sock")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if conn, err := dialSandboxBackend(context.Background(), link); err == nil {
		_ = conn.Close()
		t.Fatal("followed app-controlled socket symlink")
	}
}

func TestEgressConnectionLimitsIncludeIdleConnections(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	global := make(chan struct{}, 1)
	listener := newEgressListener(inner, 1, global)
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); accepted <- conn }()
	excess, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = excess.Close() }()
	if err := excess.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := excess.Read(make([]byte, 1)); err == nil {
		t.Fatal("excess connection stayed open")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("connection limit did not reject excess idle connection")
	}
	_ = server.Close()
	next, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	select {
	case conn := <-accepted:
		if conn == nil {
			t.Fatal("failed to reuse released slot")
		}
	case <-time.After(time.Second):
		t.Fatal("released slot was not reusable")
	}
	_ = listener.Close()
	if len(global) != 0 {
		t.Fatal("listener shutdown leaked global slots")
	}
}

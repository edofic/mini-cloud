package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("MINICLOUD_HELPER") != "1" {
		return
	}
	version := os.Getenv("VERSION")
	h := http.NewServeMux()
	h.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
		_, _ = fmt.Fprint(w, version)
	})
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), h); err != nil {
		fmt.Fprintln(os.Stderr, "helper listen:", err)
	}
	os.Exit(0)
}

func testConfig(root string) Config {
	return Config{AppsDir: root, BaseDomain: "test", AdminHost: "admin.test", DefaultIdle: Duration{200 * time.Millisecond}, ScanInterval: Duration{20 * time.Millisecond}, Ports: PortRange{31000, 31999}}
}
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}
func helperManifest(version string) Manifest {
	return Manifest{Process: &ProcessConfig{Command: []string{os.Args[0], "-test.run=TestHelperProcess"}, WorkingDirectory: ".", Environment: map[string]string{"MINICLOUD_HELPER": "1", "VERSION": version}, StartupTimeout: Duration{3 * time.Second}, ShutdownTimeout: Duration{time.Second}, Readiness: ReadinessConfig{Type: "tcp"}, Restart: RestartConfig{Policy: "on-failure", Delay: Duration{10 * time.Millisecond}, MaximumAttempts: 2}}, Idle: &Duration{200 * time.Millisecond}, Access: "public"}
}
func request(g *Gateway, host, path string) (int, string) {
	r := httptest.NewRequest("GET", "http://"+host+path, nil)
	r.Host = host
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	res := w.Result()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestProcessStartsRestartsAfterEditAndIdles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hello")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, manifestName), helperManifest("one"))
	g := NewGateway(testConfig(root))
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	code, body := request(g, "hello.test", "/")
	if code != 200 || body != "one" {
		t.Fatalf("first request: %d %q", code, body)
	}
	writeJSON(t, filepath.Join(dir, manifestName), helperManifest("two"))
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		code, body = request(g, "hello.test", "/")
		if code == 200 && body == "two" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new version unavailable: %d %q", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	a := g.apps["hello"]
	deadline = time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		state := a.state
		a.mu.Unlock()
		if state == "stopped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("app did not idle; state=%s", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	g.stopAll()
}

func TestEditDrainsOldRequestAndHoldsNewRequest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hello")
	_ = os.Mkdir(dir, 0755)
	writeJSON(t, filepath.Join(dir, manifestName), helperManifest("old"))
	g := NewGateway(testConfig(root))
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan string, 1)
	go func() { _, body := request(g, "hello.test", "/slow"); oldDone <- body }()
	deadline := time.Now().Add(time.Second)
	a := g.apps["hello"]
	for {
		a.mu.Lock()
		active := a.active
		a.mu.Unlock()
		if active == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow request did not become active")
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeJSON(t, filepath.Join(dir, manifestName), helperManifest("new"))
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	newDone := make(chan string, 1)
	go func() { _, body := request(g, "hello.test", "/"); newDone <- body }()
	select {
	case body := <-newDone:
		t.Fatalf("new request was not held: %q", body)
	case <-time.After(100 * time.Millisecond):
	}
	if body := <-oldDone; body != "old" {
		t.Fatalf("old in-flight request got %q", body)
	}
	select {
	case body := <-newDone:
		if body != "new" {
			t.Fatalf("held request got %q", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("held request was not released")
	}
	g.stopAll()
}

func TestInvalidManifestKeepsLastValid(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "hello")
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "public", "index.html"), []byte("ok"), 0644)
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{Static: &StaticConfig{Root: "public"}, Access: "public"})
	g := NewGateway(testConfig(root))
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, manifestName), []byte("{"), 0644)
	_ = g.scan()
	code, body := request(g, "hello.test", "/")
	if code != 200 || body != "ok" {
		t.Fatalf("last valid config lost: %d %q", code, body)
	}
	if g.apps["hello"].configError == "" {
		t.Fatal("config error not retained")
	}
}

func TestCGIRFCResponse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cgi")
	_ = os.Mkdir(dir, 0755)
	script := filepath.Join(dir, "handler.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\nprintf 'Status: 201 Created\\r\\nContent-Type: text/plain\\r\\n\\r\\n%s' \"$PATH_INFO\"\n"), 0755)
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{CGI: &CGIConfig{Command: []string{"./handler.sh"}, WorkingDirectory: ".", Timeout: Duration{time.Second}, MaximumConcurrency: 1}, Access: "public"})
	g := NewGateway(testConfig(root))
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	code, body := request(g, "cgi.test", "/abc")
	if code != 201 || strings.TrimSpace(body) != "/abc" {
		t.Fatalf("CGI: %d %q", code, body)
	}
}

func TestStablePort(t *testing.T) {
	g := NewGateway(testConfig(t.TempDir()))
	p1 := g.allocatePort("alpha")
	g2 := NewGateway(testConfig(t.TempDir()))
	p2 := g2.allocatePort("alpha")
	if p1 != p2 || p1 < 31000 || p1 > 31999 {
		t.Fatalf("ports %d %d", p1, p2)
	}
}

func TestAuthenticatedAppUsesVerifier(t *testing.T) {
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=good" {
			w.Header().Set("Location", "https://auth.test/")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Remote-User", "operator")
	}))
	defer verifier.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "private")
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "public", "index.html"), []byte("secret"), 0644)
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{Static: &StaticConfig{Root: "public"}})
	cfg := testConfig(root)
	cfg.Auth = AuthConfig{IdentityHeader: "Remote-User", VerifyURL: verifier.URL, CopyHeaders: []string{"Remote-User"}}
	g := NewGateway(cfg)
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://private.test/", nil)
	r.Host = "private.test"
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("anonymous status=%d", w.Code)
	}
	r = httptest.NewRequest("GET", "http://private.test/", nil)
	r.Host = "private.test"
	r.Header.Set("Cookie", "session=good")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "secret" {
		t.Fatalf("authenticated status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestAuthenticatedAppRestrictsUsersAndGroups(t *testing.T) {
	verifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Cookie") {
		case "session=admin":
			w.Header().Set("Remote-User", "operator")
			w.Header().Set("Remote-Groups", "admin, home")
		case "session=alice":
			w.Header().Set("Remote-User", "alice")
			w.Header().Set("Remote-Groups", "home")
		case "session=other":
			w.Header().Set("Remote-User", "other")
			w.Header().Set("Remote-Groups", "home")
		default:
			w.Header().Set("Location", "https://auth.test/")
			w.WriteHeader(http.StatusFound)
		}
	}))
	defer verifier.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "private")
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "public", "index.html"), []byte("secret"), 0644)
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{
		Static:       &StaticConfig{Root: "public"},
		AccessUsers:  []string{"alice"},
		AccessGroups: []string{"admin"},
	})
	cfg := testConfig(root)
	cfg.Auth = AuthConfig{
		IdentityHeader: "Remote-User",
		GroupsHeader:   "Remote-Groups",
		VerifyURL:      verifier.URL,
		CopyHeaders:    []string{"Remote-User", "Remote-Groups"},
	}
	g := NewGateway(cfg)
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name, cookie string
		want         int
	}{
		{name: "anonymous", want: http.StatusFound},
		{name: "allowed user", cookie: "session=alice", want: http.StatusOK},
		{name: "allowed group", cookie: "session=admin", want: http.StatusOK},
		{name: "denied principal", cookie: "session=other", want: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://private.test/", nil)
			r.Host = "private.test"
			if tt.cookie != "" {
				r.Header.Set("Cookie", tt.cookie)
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status=%d, want %d; body=%q", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestPublicIndexAndIcon(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "page")
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "public", "icon.svg"), []byte("<svg/>"), 0644)
	writeJSON(t, filepath.Join(dir, manifestName), Manifest{DisplayName: "My page", Description: "Example description", Icon: "public/icon.svg", Static: &StaticConfig{Root: "public"}, Access: "public"})
	cfg := testConfig(root)
	cfg.IndexHost = "apps.test"
	g := NewGateway(cfg)
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	code, body := request(g, "apps.test", "/")
	if code != 200 || !strings.Contains(body, "My page") || !strings.Contains(body, "Example description") {
		t.Fatalf("index: %d %q", code, body)
	}
	code, body = request(g, "apps.test", "/icons/page")
	if code != 200 || body != "<svg/>" {
		t.Fatalf("icon: %d %q", code, body)
	}
}

func TestIndexRequiresAuthenticationAndFiltersApps(t *testing.T) {
	root := t.TempDir()
	for _, app := range []struct {
		name, display string
		access        string
		users, groups []string
	}{
		{name: "public", display: "Public app", access: "public"},
		{name: "shared", display: "Shared app", users: []string{"alice"}, groups: []string{"admin"}},
		{name: "private", display: "Private app", groups: []string{"admin"}},
	} {
		dir := filepath.Join(root, app.name)
		_ = os.MkdirAll(filepath.Join(dir, "public"), 0755)
		_ = os.WriteFile(filepath.Join(dir, "public", "icon.svg"), []byte(app.name), 0644)
		writeJSON(t, filepath.Join(dir, manifestName), Manifest{
			DisplayName:  app.display,
			Icon:         "public/icon.svg",
			Access:       app.access,
			AccessUsers:  app.users,
			AccessGroups: app.groups,
			Static:       &StaticConfig{Root: "public"},
		})
	}
	cfg := testConfig(root)
	cfg.IndexHost = "apps.test"
	cfg.Auth = AuthConfig{IdentityHeader: "Remote-User", GroupsHeader: "Remote-Groups"}
	g := NewGateway(cfg)
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "http://apps.test/", nil)
	r.Host = "apps.test"
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous index status=%d, want %d", w.Code, http.StatusUnauthorized)
	}

	r = httptest.NewRequest("GET", "http://apps.test/", nil)
	r.Host = "apps.test"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("Remote-Groups", "home")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("index status=%d body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Public app") || !strings.Contains(body, "Shared app") {
		t.Fatalf("index omitted allowed apps: %q", body)
	}
	if strings.Contains(body, "Private app") {
		t.Fatalf("index exposed denied app: %q", body)
	}

	r = httptest.NewRequest("GET", "http://apps.test/icons/private", nil)
	r.Host = "apps.test"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("Remote-Groups", "home")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("denied icon status=%d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestCronRunsCommand(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "job")
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0755)
	output := filepath.Join(t.TempDir(), "ran")
	m := Manifest{
		Static: &StaticConfig{Root: "public"},
		Access: "public",
		Cron: []CronConfig{{
			Name: "write", Schedule: "* * * * *",
			Command: []string{"/bin/sh", "-c", `printf ran > "${OUTPUT}"`}, Environment: map[string]string{"OUTPUT": output},
			Overlap: "forbid", Missed: "skip", Timeout: Duration{time.Second},
		}},
	}
	writeJSON(t, filepath.Join(dir, manifestName), m)
	g := NewGateway(testConfig(root))
	g.ctx = t.Context()
	if err := g.scan(); err != nil {
		t.Fatal(err)
	}
	g.runDue(time.Now())
	deadline := time.Now().Add(2 * time.Second)
	for {
		if b, err := os.ReadFile(output); err == nil && string(b) == "ran" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cron command did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

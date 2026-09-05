package main

import (
	"encoding/json"
	"html/template"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type appView struct {
	Name, Host, Kind, Access, State, Error, ConfigError string
	Port, Active                                        int
	Restarts                                            int
	LastActivity                                        time.Time
	Jobs                                                map[string]string
}

func (g *Gateway) views() []appView {
	g.mu.RLock()
	apps := make([]*App, 0, len(g.apps))
	for _, a := range g.apps {
		apps = append(apps, a)
	}
	g.mu.RUnlock()
	out := make([]appView, 0, len(apps))
	for _, a := range apps {
		a.mu.Lock()
		kind := "process"
		if a.manifest.Static != nil {
			kind = "static"
		} else if a.manifest.CGI != nil {
			kind = "cgi"
		}
		host := a.manifest.Host
		if host == "" {
			host = a.name + "." + g.cfg.BaseDomain
		}
		view := appView{Name: a.name, Host: host, Kind: kind, Access: accessSummary(a.manifest), State: a.state, Error: a.stateError, ConfigError: a.configError, Port: a.port, Active: a.active, Restarts: a.restarts, LastActivity: a.lastActivity, Jobs: map[string]string{}}
		for n, j := range a.cron {
			view.Jobs[n] = j.lastResult
		}
		out = append(out, view)
		a.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func accessSummary(m Manifest) string {
	principals := make([]string, 0, 2)
	if len(m.AccessUsers) != 0 {
		principals = append(principals, "users: "+strings.Join(m.AccessUsers, ", "))
	}
	if len(m.AccessGroups) != 0 {
		principals = append(principals, "groups: "+strings.Join(m.AccessGroups, ", "))
	}
	if len(principals) == 0 {
		return m.Access
	}
	return m.Access + " (" + strings.Join(principals, "; ") + ")"
}

func (g *Gateway) serveAdmin(w http.ResponseWriter, r *http.Request) {
	if !g.authorize(w, r) {
		return
	}
	if g.cfg.Auth.IdentityHeader != "" && g.cfg.Auth.AdminIdentity != "" {
		if r.Header.Get(g.cfg.Auth.IdentityHeader) != g.cfg.Auth.AdminIdentity {
			http.Error(w, "admin authentication required", http.StatusForbidden)
			return
		}
	}
	if r.URL.Path == "/api/apps" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g.views())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTemplate.Execute(w, struct {
		Apps []appView
		Now  time.Time
	}{g.views(), time.Now()})
}

type indexApp struct {
	Name, DisplayName, Description, Host, Initial string
	HasIcon                                       bool
}

func (g *Gateway) indexApps(r *http.Request) []indexApp {
	g.mu.RLock()
	apps := make([]*App, 0, len(g.apps))
	for _, a := range g.apps {
		apps = append(apps, a)
	}
	g.mu.RUnlock()
	out := make([]indexApp, 0, len(apps))
	for _, a := range apps {
		a.mu.Lock()
		manifest := a.manifest
		if a.snapshot == "" || !g.appAccessAllowed(r, manifest) {
			a.mu.Unlock()
			continue
		}
		host := manifest.Host
		if host == "" {
			host = a.name + "." + g.cfg.BaseDomain
		}
		name := manifest.DisplayName
		if name == "" {
			name = a.name
		}
		initial := strings.ToUpper(string([]rune(name)[0]))
		out = append(out, indexApp{Name: a.name, DisplayName: name, Description: manifest.Description, Host: host, Initial: initial, HasIcon: manifest.Icon != ""})
		a.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName) })
	return out
}

func (g *Gateway) serveIndex(w http.ResponseWriter, r *http.Request) {
	if !g.authorize(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/icons/") {
		g.serveIndexIcon(w, r, strings.TrimPrefix(r.URL.Path, "/icons/"))
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, g.indexApps(r))
}

func (g *Gateway) serveIndexIcon(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	g.mu.RLock()
	a := g.apps[name]
	g.mu.RUnlock()
	if a == nil {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	manifest := a.manifest
	a.mu.Unlock()
	if !g.appAccessAllowed(r, manifest) {
		http.Error(w, "application access denied", http.StatusForbidden)
		return
	}
	icon := manifest.Icon
	if icon == "" {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(a.dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(icon)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	if typ := mime.TypeByExtension(filepath.Ext(icon)); typ != "" {
		w.Header().Set("Content-Type", typ)
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

var indexTemplate = template.Must(template.New("index").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Apps</title><style>:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;background:#0b1020;color:#eef2ff;font:15px system-ui,sans-serif}.wrap{max-width:1000px;margin:auto;padding:48px 24px}h1{font-size:1.8rem;margin:0 0 6px}.sub{color:#94a3b8;margin:0 0 26px}.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(210px,1fr));gap:12px}.tile{display:flex;align-items:center;gap:13px;padding:14px;border:1px solid #26324d;border-radius:14px;background:#121a2d;color:inherit;text-decoration:none;transition:.15s transform,.15s border-color,.15s background}.tile:hover{transform:translateY(-2px);border-color:#64748b;background:#172239}.icon{width:42px;height:42px;flex:0 0 42px;border-radius:11px;display:grid;place-items:center;background:#273451;color:#c7d2fe;font-size:19px;font-weight:700;object-fit:cover}.copy{min-width:0}.name{font-size:1rem;font-weight:650;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.desc{color:#94a3b8;font-size:.8rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;margin-top:3px}</style></head><body><main class="wrap"><h1>Apps</h1><p class="sub">Choose an app to open.</p><section class="grid">{{range .}}<a class="tile" href="https://{{.Host}}/">{{if .HasIcon}}<img class="icon" src="/icons/{{.Name}}" alt="">{{else}}<span class="icon">{{.Initial}}</span>{{end}}<div class="copy"><div class="name">{{.DisplayName}}</div>{{if .Description}}<div class="desc">{{.Description}}</div>{{end}}</div></a>{{else}}<p>No apps found.</p>{{end}}</section></main></body></html>`))

var adminTemplate = template.Must(template.New("admin").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>mini-cloud</title><style>body{font:15px system-ui;margin:2rem;background:#111827;color:#e5e7eb}table{border-collapse:collapse;width:100%}th,td{text-align:left;padding:.65rem;border-bottom:1px solid #374151}a{color:#93c5fd}.bad{color:#fca5a5}.ok{color:#86efac}code{white-space:pre-wrap}</style></head><body><h1>mini-cloud</h1><p>{{.Now.Format "2006-01-02 15:04:05 MST"}}</p><table><tr><th>app</th><th>kind</th><th>access</th><th>state</th><th>port</th><th>active</th><th>restarts</th><th>cron</th><th>errors</th></tr>{{range .Apps}}<tr><td><a href="https://{{.Host}}">{{.Name}}</a></td><td>{{.Kind}}</td><td>{{.Access}}</td><td class="{{if or .Error .ConfigError}}bad{{else}}ok{{end}}">{{.State}}</td><td>{{if .Port}}{{.Port}}{{end}}</td><td>{{.Active}}</td><td>{{.Restarts}}</td><td>{{range $name,$result := .Jobs}}<code>{{$name}}: {{$result}}</code><br>{{end}}</td><td><code>{{.ConfigError}}{{if and .ConfigError .Error}}
{{end}}{{.Error}}</code></td></tr>{{end}}</table></body></html>`))

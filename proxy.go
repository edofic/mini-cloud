package main

import (
	"net/http"
	"net/http/httputil"
)

func (a *App) proxy(w http.ResponseWriter, r *http.Request, backend string) {
	proxy := httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme = "http"
			p.Out.URL.Host = backend
			p.Out.Host = p.In.Host
			// Incoming forwarding metadata is trusted only behind the documented edge proxy.
			p.Out.Header["X-Forwarded-For"] = append([]string(nil), p.In.Header.Values("X-Forwarded-For")...)
			p.SetXForwarded()
			if proto := p.In.Header.Get("X-Forwarded-Proto"); proto != "" {
				p.Out.Header.Set("X-Forwarded-Proto", proto)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "backend unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

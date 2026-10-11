package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// This proxy lives outside app namespaces. It resolves and pins each destination
// itself, so clients cannot bypass private-address checks through DNS rebinding.
type egressPolicy struct {
	mu         sync.RWMutex
	hosts      map[string]bool
	verifyURL  string
	verifyURLs map[string]string
}

func (p *egressPolicy) set(opts *SandboxOptions, verifyURL string, verifyURLs map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hosts = map[string]bool{}
	p.verifyURL = ""
	p.verifyURLs = nil
	if opts == nil {
		return
	}
	for _, host := range opts.Network.AllowHosts {
		p.hosts[strings.ToLower(host)] = true
	}
	if opts.Network.ForwardAuth {
		p.verifyURL = verifyURL
		p.verifyURLs = make(map[string]string, len(verifyURLs))
		for host, endpoint := range verifyURLs {
			p.verifyURLs[host] = endpoint
		}
	}
}

func (p *egressPolicy) allowed(host string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.hosts["*"] || p.hosts[strings.ToLower(host)]
}

var privateNetworks = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"} {
		_, block, _ := net.ParseCIDR(cidr)
		out = append(out, block)
	}
	return out
}()

func publicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, block := range privateNetworks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

// Deny this host and directly connected subnets even when they use globally
// routable IPv6. Otherwise a public-address CONNECT could reach private Caddy.
func localDestination(ip net.IP) bool {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	for _, address := range addresses {
		local, ok := address.(*net.IPNet)
		if !ok {
			continue
		}
		if local.IP.Equal(ip) {
			return true
		}
		ones, bits := local.Mask.Size()
		if (bits == 32 && ones >= 8 || bits == 128 && ones >= 48) && local.Contains(ip) {
			return true
		}
	}
	return false
}

func (p *egressPolicy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (port != "80" && port != "443") || !p.allowed(host) {
		return nil, fmt.Errorf("destination not permitted")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("destination resolution failed")
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) || localDestination(ip.IP) || ip.Zone != "" {
			return nil, fmt.Errorf("private or special destination not permitted")
		}
	}
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		conn, e := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.IP.String(), port))
		if e == nil {
			return conn, nil
		}
		err = e
	}
	return nil, err
}

func newEgressServer(policy *egressPolicy) *http.Server {
	transport := &http.Transport{Proxy: nil, DialContext: policy.dial, MaxIdleConns: 32, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	return &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "CONNECT" {
			// CONNECT is only for HTTPS. HTTP is forwarded as HTTP, never an arbitrary tunnel.
			_, port, err := net.SplitHostPort(r.Host)
			if err != nil || port != "443" {
				http.Error(w, "destination not permitted", 403)
				return
			}
			upstream, err := policy.dial(r.Context(), "tcp", r.Host)
			if err != nil {
				http.Error(w, "destination not permitted or unavailable", 403)
				return
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				upstream.Close()
				http.Error(w, "tunneling unavailable", 500)
				return
			}
			conn, buffer, err := hijacker.Hijack()
			if err != nil {
				upstream.Close()
				return
			}
			defer conn.Close()
			defer upstream.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Minute))
			upstream.SetDeadline(time.Now().Add(5 * time.Minute))
			if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
				return
			}
			if buffer.Flush() != nil {
				return
			}
			done := make(chan struct{})
			go func() { io.Copy(upstream, buffer); upstream.(*net.TCPConn).CloseWrite(); close(done) }()
			io.Copy(conn, upstream)
			if half, ok := conn.(interface{ CloseWrite() error }); ok {
				half.CloseWrite()
			}
			<-done
			return
		}
		if r.URL.Scheme != "http" || r.URL.User != nil || r.URL.Fragment != "" {
			http.Error(w, "HTTP proxy request required", 400)
			return
		}
		if r.URL.Hostname() == "auth.mini-cloud.internal" {
			policy.mu.RLock()
			verifyURL := policy.verifyURL
			if endpoint := policy.verifyURLs[stripPort(r.Header.Get("X-Forwarded-Host"))]; endpoint != "" {
				verifyURL = endpoint
			}
			policy.mu.RUnlock()
			if verifyURL == "" || r.Method != "GET" || r.URL.Path != "/verify" || r.URL.RawQuery != "" || r.URL.Port() != "" {
				http.Error(w, "authentication endpoint not permitted", 403)
				return
			}
			target, err := url.Parse(verifyURL)
			if err != nil {
				http.Error(w, "authentication unavailable", 502)
				return
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.Director = func(out *http.Request) {
				out.URL.Scheme = target.Scheme
				out.URL.Host = target.Host
				out.URL.Path = target.Path
				out.URL.RawQuery = target.RawQuery
				out.Host = target.Host
			}
			authTransport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 10 * time.Second}
			defer authTransport.CloseIdleConnections()
			proxy.Transport = authTransport
			proxy.ServeHTTP(w, r)
			return
		}
		port := r.URL.Port()
		if port == "" {
			port = "80"
		}
		if port != "80" || !policy.allowed(r.URL.Hostname()) {
			http.Error(w, "destination not permitted", 403)
			return
		}
		proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(out *httputil.ProxyRequest) {
			out.Out.URL.Scheme = "http"
			out.Out.URL.Host = r.URL.Host
			out.Out.Host = r.URL.Host
			out.Out.Header.Del("Proxy-Authorization")
		}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "destination not permitted or unavailable", 502)
		}}
		proxy.ServeHTTP(w, r)
	})}
}

// Bound idle connections as well as active requests and CONNECT tunnels. Closing
// the listener also closes hijacked tunnels, which http.Server.Close cannot do.
type egressListener struct {
	net.Listener
	mu            sync.Mutex
	connections   map[*egressConn]bool
	slots, global chan struct{}
	closed        bool
}
type egressConn struct {
	net.Conn
	owner *egressListener
	once  sync.Once
}

func newEgressListener(inner net.Listener, limit int, global chan struct{}) *egressListener {
	return &egressListener{Listener: inner, slots: make(chan struct{}, limit), global: global, connections: map[*egressConn]bool{}}
}
func (l *egressListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.global <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
		default:
			<-l.global
			conn.Close()
			continue
		}
		wrapped := &egressConn{Conn: conn, owner: l}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			wrapped.Close()
			return nil, net.ErrClosed
		}
		l.connections[wrapped] = true
		l.mu.Unlock()
		return wrapped, nil
	}
}
func (c *egressConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.owner.mu.Lock()
		delete(c.owner.connections, c)
		c.owner.mu.Unlock()
		<-c.owner.slots
		<-c.owner.global
	})
	return err
}
func (l *egressListener) Close() error {
	err := l.Listener.Close()
	l.mu.Lock()
	l.closed = true
	conns := make([]*egressConn, 0, len(l.connections))
	for conn := range l.connections {
		conns = append(conns, conn)
	}
	l.mu.Unlock()
	for _, conn := range conns {
		conn.Close()
	}
	return err
}

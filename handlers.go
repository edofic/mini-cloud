package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *App) serveStatic(w http.ResponseWriter, r *http.Request, c *StaticConfig) {
	rootPath, err := filepath.Abs(resolveWithin(a.dir, c.Root))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "."
	}
	resolved, err := confinedStaticPath(rootPath, name)
	if err != nil && c.SPAFallback != "" {
		name = c.SPAFallback
		resolved, err = confinedStaticPath(rootPath, name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		indexPath, indexErr := confinedStaticPath(rootPath, filepath.Join(name, "index.html"))
		if indexErr == nil {
			index, openErr := os.Open(indexPath)
			if openErr != nil {
				indexErr = openErr
			}
			var ii os.FileInfo
			var statErr error
			if index != nil {
				defer func() { _ = index.Close() }()
				ii, statErr = index.Stat()
			}
			if statErr == nil && ii.Mode().IsRegular() {
				_ = f.Close()
				f = index
				info = ii
				name = filepath.Join(name, "index.html")
			} else {
				_ = index.Close()
				indexErr = os.ErrNotExist
			}
		}
		if indexErr != nil {
			if !c.Browse {
				http.NotFound(w, r)
				return
			}
			// An index entry that exists but escapes the root is an invalid
			// request, rather than a reason to expose a directory listing.
			if _, statErr := os.Lstat(filepath.Join(resolved, "index.html")); statErr == nil {
				http.NotFound(w, r)
				return
			}
			serveStaticDirectory(w, r, resolved, name)
			return
		}
	}
	if !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if typ := mime.TypeByExtension(filepath.Ext(name)); typ != "" {
		w.Header().Set("Content-Type", typ)
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func serveStaticDirectory(w http.ResponseWriter, r *http.Request, dir, name string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><meta name=viewport content=width=device-width><pre>\n")
	for _, entry := range entries {
		label := entry.Name()
		href := filepath.Join("/", name, label)
		if entry.IsDir() {
			href += "/"
		}
		_, _ = io.WriteString(w, `<a href="`+html.EscapeString(href)+`">`+html.EscapeString(label)+"</a>\n")
	}
	_, _ = io.WriteString(w, "</pre>\n")
}

// confinedStaticPath resolves links before opening a path and rejects any
// target outside the configured static root. The final open is deliberately
// performed only after this check so index and fallback files receive the same
// policy as directly requested files.
func confinedStaticPath(root, name string) (string, error) {
	candidate := filepath.Join(root, filepath.FromSlash(name))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !within(root, resolved) {
		return "", os.ErrPermission
	}
	return resolved, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (a *App) serveCGI(w http.ResponseWriter, r *http.Request, c *CGIConfig) {
	a.mu.Lock()
	sem := a.cgiSem
	a.mu.Unlock()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-r.Context().Done():
		return
	}
	requestCtx, requestCancel := a.gw.requestContext(r.Context())
	defer requestCancel()
	ctx, cancel := context.WithTimeout(requestCtx, c.Timeout.Duration)
	defer cancel()
	extra := map[string]string{"APP_NAME": a.name}
	env, err := buildEnv(a.dir, c.WorkingDirectory, c.EnvironmentFiles, c.Environment, extra)
	if err != nil {
		http.Error(w, "CGI environment error", 500)
		return
	}
	args := interpolate(c.Command, env)
	path := args[0]
	if strings.Contains(path, string(filepath.Separator)) && !filepath.IsAbs(path) {
		path = filepath.Join(resolveWithin(a.dir, c.WorkingDirectory), path)
	}
	cmd := exec.Command(path, args[1:]...)
	cmd.Dir = resolveWithin(a.dir, c.WorkingDirectory)
	cmd.Stdin = r.Body
	env["GATEWAY_INTERFACE"] = "CGI/1.1"
	env["SERVER_PROTOCOL"] = r.Proto
	env["SERVER_SOFTWARE"] = "mini-cloud"
	env["REQUEST_METHOD"] = r.Method
	env["QUERY_STRING"] = r.URL.RawQuery
	env["REQUEST_URI"] = r.URL.RequestURI()
	env["SCRIPT_NAME"] = ""
	env["PATH_INFO"] = r.URL.Path
	env["SERVER_NAME"] = stripPort(r.Host)
	_, serverPort, splitErr := net.SplitHostPort(r.Host)
	if splitErr != nil {
		serverPort = "80"
	}
	env["SERVER_PORT"] = serverPort
	env["REMOTE_ADDR"] = r.RemoteAddr
	if r.ContentLength >= 0 {
		env["CONTENT_LENGTH"] = strconv.FormatInt(r.ContentLength, 10)
	}
	if v := r.Header.Get("Content-Type"); v != "" {
		env["CONTENT_TYPE"] = v
	}
	for k, v := range r.Header {
		key := "HTTP_" + strings.ReplaceAll(strings.ToUpper(k), "-", "_")
		env[key] = strings.Join(v, ",")
	}
	cmd.Env = envList(env)
	out, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "CGI setup failed", 500)
		return
	}
	cmd.Stderr = &logWriter{prefix: "app=" + a.name + " runtime=cgi stream=stderr "}
	if err = a.gw.startChild(ctx, cmd); err != nil {
		http.Error(w, "CGI start failed", http.StatusBadGateway)
		return
	}
	br := bufio.NewReader(out)
	hdr, status, err := readCGIHeaders(br)
	if err != nil {
		cancel()
		_ = a.gw.waitChild(cmd)
		http.Error(w, "invalid CGI response", http.StatusBadGateway)
		return
	}
	hdr.Del("Status")
	for k, v := range hdr {
		for _, x := range v {
			w.Header().Add(k, x)
		}
	}
	w.WriteHeader(status)
	_, copyErr := io.Copy(w, br)
	waitErr := a.gw.waitChild(cmd)
	if copyErr != nil || waitErr != nil {
		log.Printf("app=%s runtime=cgi copy_error=%v exit_error=%v", a.name, copyErr, waitErr)
	}
}

// Read only a bounded header block; the same buffered reader retains the body.
func readCGIHeaders(br *bufio.Reader) (textproto.MIMEHeader, int, error) {
	const maximum = 64 << 10
	var block bytes.Buffer
	for {
		line, err := br.ReadSlice('\n')
		if block.Len()+len(line) > maximum {
			return nil, 0, errors.New("CGI headers exceed 64 KiB")
		}
		block.Write(line)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
			break
		}
	}
	hdr, err := textproto.NewReader(bufio.NewReader(&block)).ReadMIMEHeader()
	if err != nil {
		return nil, 0, err
	}
	status := http.StatusOK
	if value := hdr.Get("Status"); value != "" {
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return nil, 0, errors.New("invalid CGI status")
		}
		status, err = strconv.Atoi(fields[0])
		if err != nil || status < 200 || status > 599 {
			return nil, 0, errors.New("invalid CGI status")
		}
	} else if hdr.Get("Location") != "" {
		status = http.StatusFound
	}
	return hdr, status, nil
}

package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// All children are registered before they become visible to shutdown. Wait is
// paired exactly once with a successful start, including failed readiness.
func (g *Gateway) startChild(ctx context.Context, cmd *exec.Cmd) error {
	g.childMu.Lock()
	defer g.childMu.Unlock()
	if g.closing {
		return fmt.Errorf("gateway is shutting down")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	if g.children == nil {
		g.children = map[*exec.Cmd]func() bool{}
	}
	g.childrenWG.Add(1)
	g.children[cmd] = context.AfterFunc(ctx, func() { g.signalChild(cmd, syscall.SIGKILL) })
	return nil
}
func (g *Gateway) waitChild(cmd *exec.Cmd) error {
	err := cmd.Wait()
	g.childMu.Lock()
	// A parent exiting does not mean its descendants have exited. Do not leave
	// inherited listeners or pipes behind, even after a successful command.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if stop, ok := g.children[cmd]; ok {
		stop()
		delete(g.children, cmd)
		g.childrenWG.Done()
	}
	g.childMu.Unlock()
	return err
}
func (g *Gateway) signalChild(cmd *exec.Cmd, signal syscall.Signal) {
	g.childMu.Lock()
	defer g.childMu.Unlock()
	if _, ok := g.children[cmd]; ok {
		_ = syscall.Kill(-cmd.Process.Pid, signal)
	}
}
func (g *Gateway) killChildren() {
	g.childMu.Lock()
	defer g.childMu.Unlock()
	for cmd := range g.children {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
func (g *Gateway) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if g.ctx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(g.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}
func appCommand(appDir, work string, args []string) *exec.Cmd {
	path := args[0]
	if strings.ContainsRune(path, filepath.Separator) && !filepath.IsAbs(path) {
		path = filepath.Join(resolveWithin(appDir, work), path)
	}
	cmd := exec.Command(path, args[1:]...)
	cmd.Dir = resolveWithin(appDir, work)
	return cmd
}

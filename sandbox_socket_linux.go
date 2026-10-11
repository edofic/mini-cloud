//go:build linux

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"syscall"
)

// Pin the socket inode without following an app-controlled symlink. Opening with
// O_PATH works for Unix sockets, and Linux resolves /proc/self/fd through the
// pinned inode even if its directory entry is replaced during the connect.
func dialSandboxBackend(ctx context.Context, path string) (net.Conn, error) {
	const oPath = 0x200000
	fd, err := syscall.Open(path, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("backend must be a Unix socket, not a symlink or file")
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", fmt.Sprintf("/proc/self/fd/%d", fd))
}

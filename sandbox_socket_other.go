//go:build !linux

package main

import (
	"context"
	"fmt"
	"net"
)

func dialSandboxBackend(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("hardened sandbox requires Linux")
}

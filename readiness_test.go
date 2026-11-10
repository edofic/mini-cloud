package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Use the same server over both transports to exercise both readiness loops.
func readinessServer(t *testing.T, network string, handler http.Handler) func(context.Context, ReadinessConfig) error {
	t.Helper()
	address := "127.0.0.1:0"
	if network == "unix" {
		dir, err := os.MkdirTemp("", "mc-ready-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		address = filepath.Join(dir, "app.sock")
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	if network == "unix" {
		return func(ctx context.Context, cfg ReadinessConfig) error { return waitReadyUnix(ctx, address, cfg) }
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return func(ctx context.Context, cfg ReadinessConfig) error { return waitReady(ctx, port, cfg) }
}

func TestReadinessPollingInterval(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		for _, interval := range []time.Duration{0, time.Millisecond, 150 * time.Millisecond} {
			t.Run(network+"/"+interval.String(), func(t *testing.T) {
				// More than the old 100 ms default catches a hardcoded retry delay;
				// the 1 ms case also exercises aggressive polling without tight deadlines.
				probes := make(chan time.Time, 2)
				wait := readinessServer(t, network, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/ready" {
						t.Errorf("path = %q", r.URL.Path)
					}
					probes <- time.Now()
					if len(probes) == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				}))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cfg := ReadinessConfig{Type: "http", Path: "/ready", Interval: Duration{interval}}
				if err := wait(ctx, cfg); err != nil {
					t.Fatal(err)
				}
				first, second := <-probes, <-probes
				want := interval
				if want == 0 {
					want = 100 * time.Millisecond
				}
				if gap := second.Sub(first); gap < want {
					t.Fatalf("probe gap = %s, want at least %s", gap, want)
				}
			})
		}
	}
}

func TestReadinessCancellationDuringLongInterval(t *testing.T) {
	for _, network := range []string{"tcp", "unix"} {
		t.Run(network, func(t *testing.T) {
			probed := make(chan struct{}, 1)
			wait := readinessServer(t, network, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case probed <- struct{}{}:
				default:
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- wait(ctx, ReadinessConfig{Type: "http", Interval: Duration{time.Hour}}) }()
			select {
			case <-probed:
			case <-time.After(5 * time.Second):
				t.Fatal("initial probe did not run immediately")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want cancellation", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation waited for polling interval")
			}
		})
	}
}

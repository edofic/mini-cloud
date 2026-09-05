package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestAppCommandWithoutSandbox(t *testing.T) {
	appDir := t.TempDir()
	cmd := appCommand(appDir, "server", []string{"./run", "one"}, "")
	wantPath := filepath.Join(appDir, "server", "run")
	if cmd.Path != wantPath || cmd.Dir != filepath.Join(appDir, "server") {
		t.Fatalf("command path=%q dir=%q", cmd.Path, cmd.Dir)
	}
}

func TestAppCommandBuildsBubblewrapInvocation(t *testing.T) {
	appDir := t.TempDir()
	cmd := appCommand(appDir, "server", []string{"./run", "one"}, "bubblewrap")
	wantTail := []string{
		"--bind", appDir, appDir,
		"--chdir", filepath.Join(appDir, "server"),
		"--", filepath.Join(appDir, "server", "run"), "one",
	}
	if filepath.Base(cmd.Path) != "bwrap" || !slices.Equal(cmd.Args[len(cmd.Args)-len(wantTail):], wantTail) {
		t.Fatalf("bubblewrap command path=%q args=%q", cmd.Path, cmd.Args)
	}
	for _, flag := range []string{"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--share-net"} {
		if !slices.Contains(cmd.Args, flag) {
			t.Errorf("bubblewrap args lack %s: %q", flag, cmd.Args)
		}
	}
}

func TestBubblewrapCommandFilesystemIsolation(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed")
	}
	appDir := t.TempDir()
	marker := filepath.Join("/tmp", "mini-cloud-bwrap-"+filepath.Base(filepath.Dir(appDir)))
	hostWritable, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := appCommand(appDir, ".", []string{"/bin/sh", "-c", `touch app-write && touch "$1" && test ! -w "$2"`, "sh", marker, hostWritable}, "bubblewrap")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bubblewrap command: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(appDir, "app-write")); err != nil {
		t.Fatalf("app directory was not writable: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("private temporary file escaped sandbox: %v", err)
	}
}

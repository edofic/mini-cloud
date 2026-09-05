# macOS

mini-cloud runs natively on Apple Silicon and Intel Macs. Bubblewrap sandboxing is Linux-only, so macOS application manifests must omit `sandbox`.

## Build and run

On Apple Silicon, enter the Nix development shell and build the package from the repository root:

```sh
nix develop
nix build
./result/bin/mini-cloud -config "$PWD/mini-cloud.example.json"
```

The pinned unstable Nixpkgs no longer supports Intel macOS, so the flake does not export an `x86_64-darwin` package or development shell. On an Intel Mac, or without Nix, install Go 1.27, Python 3, a POSIX shell, and curl, then use `go build`. The Go source and example applications support both Mac architectures.

## Run as a LaunchAgent

For a single-user machine, create `~/Library/LaunchAgents/com.example.mini-cloud.plist` with absolute paths appropriate for that user:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.example.mini-cloud</string>
  <key>ProgramArguments</key>
  <array>
    <string>/absolute/path/to/mini-cloud</string>
    <string>-config</string>
    <string>/absolute/path/to/mini-cloud.json</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/absolute/path/to/working-directory</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/absolute/path/to/logs/mini-cloud.log</string>
  <key>StandardErrorPath</key>
  <string>/absolute/path/to/logs/mini-cloud.log</string>
</dict>
</plist>
```

Create the log directory first, then load the agent with:

```sh
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.example.mini-cloud.plist"
```

The configured app directory and log directory must be writable by the logged-in user. Commands inherit the explicit `PATH` above; add any application runtimes they require. A LaunchAgent runs only while that user is logged in. A system-wide deployment can instead use a LaunchDaemon configured to run as a dedicated user.

## Application notes

Python's `http.server` resolves the bound address back to a hostname during startup. Reverse DNS for loopback can stall for tens of seconds on hosts with slow or broken resolvers, which exceeds the example's startup timeout. The bundled example server therefore skips that lookup and uses the literal bind address. The gateway itself only dials literal addresses and needs no such workaround; keep custom app servers free of startup-time DNS lookups for the same reason.

## Process cleanup limitation

Graceful gateway shutdown on macOS sends SIGTERM and then SIGKILL to each application's process group, matching normal Linux behavior. If the gateway crashes or is killed with SIGKILL, however, descendant application processes can survive. Linux deployments close this gap with systemd cgroup cleanup; launchd's process-group cleanup does not cover the separate process groups mini-cloud creates for applications.

Until crash cleanup is implemented for macOS, check for and terminate orphaned application processes after an abnormal gateway exit before restarting it. This is a known lifecycle gap and the remaining macOS support TODO. A complete solution must preserve per-application group signaling during normal operation while giving the service supervisor or a crash-recovery helper a reliable way to identify and terminate every descendant.

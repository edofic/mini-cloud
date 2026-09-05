# Architecture

`mini-cloud` is a resident HTTP gateway, process supervisor, directory watcher, CGI runner, static server, and cron scheduler. It runs as one trusted user. In production, a reverse proxy such as Caddy handles TLS, public ports, and authentication integration.

```text
clients -> Caddy -> mini-cloud -> static files
                              -> one CGI process per request
                              -> loopback HTTP child processes
```

Each immediate child of `apps_dir` is identified by its directory name. A child is an app only when it contains `mini-cloud.json`. Files remain in place and mutable; the gateway never copies, builds, versions, releases, or rolls them back.

Command-based apps may opt into a fixed bubblewrap namespace preset. The gateway still directly creates and supervises those children; it does not create images, copy application files, or add a release lifecycle. The app directory is bind-mounted writable and the host supplies read-only runtimes and libraries.

The last successfully parsed manifest remains active. An invalid edit is logged and displayed on the admin page without breaking the running configuration.

## Runtime types

- `process`: an HTTP application launched on demand on a stable port.
- `static`: live files served from a configured subdirectory.
- `cgi`: an RFC 3875-style command started for every request.

## Process lifecycle

```text
stopped -> starting -> running -> stopping -> stopped
                \         |
                 `-> failed <--- unexpected exit
```

The first request starts a stopped app and waits for readiness. Concurrent requests share that start. Every proxied request holds an activity lease, including upgraded WebSocket connections. After the final request finishes, the idle timer begins. Cron does not count as activity and does not start the web process. An idle duration of zero disables idle shutdown. See [failure recovery](operations.md#failure-recovery) for retry policies and current limitations.

An app file change initiates a drain. Existing requests continue on the old process and new requests wait. When the active count reaches zero, the gateway terminates the old process group, starts the changed command on the same stable port, waits for readiness, and releases held requests.

## Stable ports

The directory name is hashed into the configured range using FNV-1a. Collisions and occupied ports are resolved by scanning forward. Directories are discovered lexically, so a stable set of apps receives repeatable assignments. The assignment remains reserved until gateway restart.

Apps receive `PORT` and `LISTEN_ADDRESS`. Command arguments support `${NAME}` interpolation after environment files and manifest values are loaded.

## Systemd boundary

Production integration should supervise the gateway as one system service with a dedicated unprivileged user, restart it after failure, and clean up its entire cgroup. There are deliberately no per-app units or timers. A gateway restart kills all descendants and begins with process apps stopped, avoiding unsafe orphan adoption.

## Non-goals

The gateway does not provide builds, deployments, releases, rollback, migrations, backups, full containers or images, DNS, TLS, distributed scheduling, resource quotas, or log storage. The optional bubblewrap preset is deliberately only lightweight process and write isolation.

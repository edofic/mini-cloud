# Detailed setup and use

## Install and configure

Build from a checkout with Go 1.27:

```sh
go build -o mini-cloud .
./mini-cloud -config /etc/mini-cloud.json
```

The configuration needs an `apps_dir`; see [gateway configuration](gateway-configuration.md) for defaults and authentication. Each immediate directory below it becomes an app when it contains `mini-cloud.json`. The directory itself is the app's mutable source of truth. mini-cloud does not copy, build, version, deploy, or roll back it.

At startup the gateway writes `AGENTS.md` and `.agents/skills/mini-cloud-app` below `apps_dir`. These generated files describe the configured hosts and paths to coding agents. Edit the templates in the source tree, not generated files. A failed managed-file write prevents startup.

## First application

In your configured `apps_dir`, create a `notes` directory with this layout:

```text
<apps_dir>/notes/
├── mini-cloud.json
└── public/
    └── index.html
```

Write `notes/mini-cloud.json`:

```json
{
  "access": "public",
  "static": { "root": "public" }
}
```

Put `index.html` in `notes/public/`, then request `notes.<base_domain>`. With the repository example configuration running, test without DNS using:

```sh
curl -H 'Host: notes.apps.test' http://127.0.0.1:9080/
```

Allow one scan interval for discovery. Changes to static files are visible on the next request. Followed symlinks must remain inside the static root.

For an HTTP process, use an argument array and bind the supplied port:

```json
{
  "access": "authenticated",
  "process": {
    "command": ["./server", "--listen", "${LISTEN_ADDRESS}"],
    "readiness": { "type": "http", "path": "/health" }
  }
}
```

The first request starts the process and waits for readiness. Concurrent cold requests share the start. A file change lets active requests finish, holds new requests, stops the old process group, and starts the new manifest. A process with no active requests stops after `idle` (or the gateway default); `idle: "0s"` disables this.

Add `"sandbox": "bubblewrap"` at the manifest's top level to place process, CGI, and cron commands in the optional lightweight filesystem and process sandbox. The app directory remains live and writable, installed host runtimes remain visible read-only, and networking is shared. Install bubblewrap first and see the [security limitations](security.md#optional-bubblewrap-sandbox).

## Access, index, and admin

Public apps skip verification. Authenticated apps use the configured identity header or verifier endpoint. `access_users` and `access_groups` are exact-match allow lists with OR semantics. The app index filters entries using those same rules. With authentication enabled, the admin host requires authentication; set `admin_identity` to restrict it to one identity. Keep the listener on loopback and strip client-supplied identity headers at the proxy.

The admin page and `/api/apps` show state, port, active requests, restart information, cron results, and manifest/process errors. They do not control processes or deploy files.

## CGI and cron

CGI commands receive an RFC 3875 environment, request body on stdin, and MIME headers/body on stdout. A CGI command is bounded by its timeout and concurrency limit. Cron uses five fields in the gateway's local timezone; missed runs are skipped and overlap is `forbid`, `allow`, or `replace`.

## Logs and troubleshooting

Gateway events, process-app output, and cron output go through the gateway logger to stderr (the systemd journal in a service). CGI stdout forms the HTTP response; CGI stderr is logged. Look for `config_error`, readiness failures, port conflicts, and `event=exit`. Common causes are a missing interpreter or `bwrap` on `PATH`, user namespaces disabled for a sandboxed app, a process not binding `127.0.0.1:$PORT`, invalid JSON, or a protected request missing the verifier/proxy identity.

Back up application directories and data with your own tools. Upgrading the gateway means replacing its binary or Nix derivation and restarting the one service; applications remain in place.

## Production boundary

Use a TLS reverse proxy for DNS, certificates, public ports, and authentication. It must remove every client-provided identity/group header before forwarding and should proxy to loopback. The gateway executes trusted manifest commands as its service user; use separate users or another execution layer for untrusted code.

For NixOS, use the flake module described in [NixOS setup](nixos.md). For a container deployment, mount the mutable app directory and configuration as described in [Docker](docker.md).

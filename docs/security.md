# Security model

The gateway executes user-owned manifests with its own Unix authority. Anyone able to modify `apps_dir` can run arbitrary code as that user. This is intentional; mini-cloud is not multi-tenant isolation.

Keep the listener on loopback behind a trusted reverse proxy such as Caddy. The proxy must also set trustworthy forwarding metadata, including `X-Forwarded-For` and `X-Forwarded-Proto`. Apps are expected to bind loopback, but the gateway does not inspect or enforce their listeners; the host firewall is the network boundary.

Public apps require no identity. Authenticated apps accept a trusted configured identity header or, when that header is absent, call the configured verification endpoint. Manifest `access_users` and `access_groups` restrictions are checked only after authentication. The app directory itself requires authentication and filters its listing and icons with those restrictions. The gateway cannot tell forged identity or group headers from trusted ones, so Caddy must strip every copied authentication header supplied by clients before proxying. Do not expose the gateway directly when header authentication is active.

Environment files support `KEY=VALUE`, comments beginning with `#`, and optional matching quotes. They are not shell programs. Store them mode `0600`. Their contents are not intentionally logged.

Static paths and symlinks remain confined to the static root. Use a dedicated public-assets directory: files such as `.env` and `mini-cloud.json` are not automatically hidden if placed under that root. Public app requests and authenticated apps' `public_paths` exceptions on the ordinary listener skip gateway verification and clear identity headers. Apps must enforce their own API credentials on exempt API routes. Exceptions do not weaken the separate public-ingress policy. OAuth callback secrets can be excluded from verifier metadata with `auth_redact_query_paths`; verifier requests never include application Authorization or Referer headers. Process and CGI commands are trusted manifest content and may intentionally address other paths. Ignore writable databases, caches, sockets, and generated files to avoid restart loops.

Children use separate Unix process groups. By default, they otherwise have the same operating-system access as the gateway user.

## Optional bubblewrap sandbox

On Linux, an app manifest can set `"sandbox": "bubblewrap"`. All of that app's process, CGI, and cron commands then run with private user, PID, IPC, UTS, and mount namespaces; a private cgroup namespace where supported; private `/tmp`, `/proc`, and `/dev`; a read-only view of the host filesystem; and a writable bind mount of the authoritative app directory. Networking remains shared so an HTTP process can bind its assigned host-loopback port. Static responses are served by the gateway itself and do not enter a sandbox. macOS does not provide Bubblewrap, and the gateway rejects manifests that request it there.

This “containers lite” preset reduces accidental or compromised writes outside the app directory and limits process visibility. It does not hide host files from the app, isolate the network, filter system calls, limit CPU or memory, create a separate service identity, or prevent apps from affecting each other through shared resources. App-directory symlinks into the host remain subject to the target's read-only mount. Continue to treat every manifest author as trusted; use separate Unix users, cgroups, firewall policy, or a full container/VM boundary for untrusted or mutually distrusting workloads.

The gateway invokes `bwrap` from its `PATH`. If it is missing or the kernel/service policy prohibits unprivileged user namespaces, sandboxed commands fail to start while unsandboxed apps continue to work. Keep bubblewrap patched through the host package manager. The NixOS module and project container image include it by default.

## Hardened sandbox

The opt-in `bubblewrap-hardened` profile hides host files, makes app code read-only,
restricts writable directories, isolates networking and filters HTTP(S) egress.
It also uses Unix backend sockets with safe pinned connections. See
[Hardened Bubblewrap](sandboxing.md) for policy, default-deny networking, deployment
trust and remaining shared-kernel/resource limitations. Public authentication is
an ingress responsibility, independent of manifest `access`.

## Constrained public listener

The optional [public ingress socket](gateway-configuration.md#constrained-public-ingress)
accepts only explicitly mapped hostnames and hardened process/CGI apps, and
verifies every request with the configured authorization service. It strips
edge identity headers instead of trusting them and keeps the full index/admin
on the ordinary private listener. Provision its directory with limited proxy
group access and share only that socket directory with an edge container.
An exposed or compromised ordinary loopback bridge would bypass these checks.
The edge still sees session cookies and application traffic, so its compromise
can steal valid sessions. The login portal remains a public authentication
surface. Neither listener turns trusted mutable manifests into multi-tenant
deployment permissions.

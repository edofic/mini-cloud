# Security model

The gateway executes user-owned manifests with its own Unix authority. Anyone able to modify `apps_dir` can run arbitrary code as that user. This is intentional; mini-cloud is not multi-tenant isolation.

Keep the listener on loopback behind a trusted reverse proxy such as Caddy. The proxy must also set trustworthy forwarding metadata, including `X-Forwarded-For` and `X-Forwarded-Proto`. Apps are expected to bind loopback, but the gateway does not inspect or enforce their listeners; the host firewall is the network boundary.

Public apps require no identity. Authenticated apps accept a trusted configured identity header or, when that header is absent, call the configured verification endpoint. Manifest `access_users` and `access_groups` restrictions are checked only after authentication. The app directory itself requires authentication and filters its listing and icons with those restrictions. The gateway cannot tell forged identity or group headers from trusted ones, so Caddy must strip every copied authentication header supplied by clients before proxying. Do not expose the gateway directly when header authentication is active.

Environment files support `KEY=VALUE`, comments beginning with `#`, and optional matching quotes. They are not shell programs. Store them mode `0600`. Their contents are not intentionally logged.

Static paths and symlinks remain confined to the static root. Use a dedicated public-assets directory: files such as `.env` and `mini-cloud.json` are not automatically hidden if placed under that root. Public app requests skip gateway verification; applications must not treat an incoming identity header as proof that the gateway authenticated that request. Process and CGI commands are trusted manifest content and may intentionally address other paths. Ignore writable databases, caches, sockets, and generated files to avoid restart loops.

Children use separate Unix process groups, but there are no per-app cgroups, namespaces, syscall filters, resource limits, or filesystem sandboxes. Use another Unix user or execution layer for untrusted code.

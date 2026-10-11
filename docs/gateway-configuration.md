# Gateway configuration

Gateway configuration is loaded once at startup. Restart the gateway to apply changes; application manifests and files are watched through native filesystem notifications. With no `-config` argument, the gateway reads `mini-cloud.json` from its working directory.

Start with:

```sh
mini-cloud -config /path/to/mini-cloud.json
```

```json
{
  "listen": "127.0.0.1:9080",
  "apps_dir": "/var/lib/mini-cloud/apps",
  "base_domain": "apps.example.test",
  "index_host": "apps.example.test",
  "admin_host": "admin.apps.example.test",
  "default_idle": "5m",
  "ports": { "start": 20000, "end": 29999 },
  "auth": {
    "timeout": "5s",
    "identity_header": "Remote-User",
    "groups_header": "Remote-Groups",
    "admin_identity": "operator",
    "verify_url": "http://127.0.0.1:9091/api/authz/forward-auth",
    "copy_headers": ["Remote-User", "Remote-Groups", "Remote-Email", "Remote-Name"]
  }
}
```

## Fields and defaults

Durations are JSON strings such as `"500ms"`, `"30s"`, and `"5m"`. Hostnames contain neither a scheme nor a port.

| Field | Default | Meaning |
| --- | --- | --- |
| `listen` | `127.0.0.1:9080` | HTTP bind address. |
| `apps_dir` | Required | Mutable app root; relative paths resolve from the gateway's working directory, not the config file. |
| `base_domain` | `apps.localhost` | Suffix for derived app hostnames. |
| `index_host` | `apps.localhost` | App directory hostname. |
| `admin_host` | `admin.apps.localhost` | Operational overview hostname; must differ from `index_host`. |
| `default_idle` | `5m` | Process idle timeout; `"0s"` disables it. |
| `ports.start`, `ports.end` | `20000`, `29999` | Inclusive backend range, within 1024–65535. |
| `auth.timeout` | `5s` | Positive verifier request timeout. |
| `auth.identity_header` | `Remote-User` | Trusted identity header; an explicit empty string disables authentication. |
| `auth.groups_header` | `Remote-Groups` | Comma-separated groups header. |
| `auth.admin_identity` | Empty | Restrict admin access to this exact identity when set. |
| `auth.verify_url` | Empty | Optional HTTP(S) authorization endpoint. |
| `auth.copy_headers` | `Remote-User`, `Remote-Groups`, `Remote-Email`, `Remote-Name` | Verifier response headers copied into the application request. |

Changing `base_domain` does not change the default index or admin hostnames; set all three together. App hostnames must not collide with either gateway hostname or another app. DNS and TLS are configured separately.

`listen` defaults to `127.0.0.1:9080`; keep it on loopback behind Caddy. `apps_dir` is required. `base_domain` makes directory `notes` available as `notes.<base_domain>` unless its manifest overrides the host. `index_host` serves the authenticated app directory, filtered to public apps and authenticated apps allowed by each manifest. Its icon endpoints enforce the same policy. `admin_host` serves the operational overview and `/api/apps`.

On startup, after this configuration has been parsed and validated, mini-cloud renders its embedded `AGENTS.md` and `mini-cloud-app` skill templates into `apps_dir`. If the gateway's working directory is a mini-cloud source checkout, the generated skill links to that local checkout; otherwise it links to <https://github.com/edofic/mini-cloud>. Existing generated paths are managed by mini-cloud and replaced when their contents differ, so customize the checked-in templates rather than deployed copies. The write is atomic, and failure to generate any managed file prevents the gateway from starting.

Filesystem notifications are debounced briefly, then application state is reconciled from disk. The watcher uses inotify on Linux and kqueue on macOS. `default_idle` defaults to five minutes; zero disables idle shutdown. `ports` is an inclusive stable backend range and must begin above 1023.

The former `scan_interval` setting has been removed because the gateway no longer polls. Existing values are ignored and should be removed from configuration files.

## Hardened sandbox runtime

The optional `sandbox` object configures `runtime_paths`, `runtime_paths_file`,
and `egress_connection_limit` for apps using `bubblewrap-hardened`. These are
trusted gateway-wide deployment settings and require a restart. See
[Hardened Bubblewrap](sandboxing.md) for fields, defaults, Nix closures and app
requirements. The compatible legacy `bubblewrap` profile does not need them.

## Automatic app guidance

On every discovery scan, including startup and later filesystem notifications, each immediate directory containing `mini-cloud.json` receives:

- An `AGENTS.md` section delimited by `<!-- mini-cloud:begin -->` and `<!-- mini-cloud:end -->`. The section tells agents to read `../AGENTS.md` and use the shared skill. An existing `AGENTS.override.md` receives the same section because Codex prefers it to `AGENTS.md`; mini-cloud never creates an override.
- A relative symlink at `.agents/skills/mini-cloud-app` pointing to `../../../.agents/skills/mini-cloud-app`, the generated shared skill under `apps_dir`.

You can start an agent directly in the app folder, even when the app is its own Git repository. App-specific instructions outside the marked section remain byte-for-byte intact, and existing instruction-file permissions are preserved. Missing files and links are recreated on the next scan; matching files and links are left untouched. Shared skill updates reach all app links without copying files into apps. Customize the shared guidance and the per-app section through the checked-in templates under `templates/apps-root`.

Instruction files must be regular files, and `.agents` and `.agents/skills` must be real directories. A conflicting skill path, malformed or duplicated section markers, or a write failure is preserved and reported in logs and the app's admin errors (`GuidanceError` in `/api/apps`). Guidance errors do not prevent app serving or manifest updates, and scans retry setup after filesystem changes. Apps with invalid manifests also receive guidance for troubleshooting.

`AGENTS.md`, `AGENTS.override.md`, `.agents`, and mini-cloud's temporary managed files are always excluded from process restart detection. Other instruction or skill locations are not managed. Removing an app or manifest does not delete generated guidance. App repositories may track their instruction files and the relative skill symlink; the shared skill's contents remain owned by mini-cloud.

## Authentication

`auth.identity_header` is the trusted authenticated identity and `auth.groups_header` is the comma-separated group list used by manifest `access_groups`; they default to `Remote-User` and `Remote-Groups`. When the identity header is absent and `verify_url` is configured, mini-cloud forwards the request headers and original request metadata to that authorization endpoint. A successful response must return the identity header; fields listed in `copy_headers` are added to the application request. Include the configured identity header in `copy_headers`; a successful verifier response without a copied identity produces 502. Include the configured groups header when any manifest uses group restrictions. Authorization redirects and failures are returned to the client without being followed. Without either a trusted identity header on the request or `verify_url`, an authenticated app returns 401. A verified identity outside the manifest allow lists receives 403. An empty identity-header setting disables authentication and manifest principal checks for local development. When authentication is enabled and `admin_identity` is nonempty, the admin host requires that exact trusted or verifier-supplied identity.

For a private wildcard ingress serving public and protected apps, Caddy should permit anonymous requests to reach the ordinary loopback listener. That listener skips verification for public apps and declared `public_paths`, clears identity headers on those requests, and calls `verify_url` for protected paths. Verifier requests omit Authorization and Referer; successful verification forwards session Set-Cookie headers. See [manifest authentication routing](application-manifest.md) for path matching and OAuth query redaction. Keep it private and strip client-supplied identity headers.

## Constrained public ingress

On Linux, optionally provision a separate Unix socket for an Internet-facing
proxy. It shares the existing gateway and app instances; it never serves the
index or admin host:

```json
{
  "public_ingress": {
    "socket_path": "/run/mini-cloud-public/apps.sock",
    "verify_url": "http://127.0.0.1:9092/api/authz/forward-auth",
    "hosts": {
      "shopping.apps.example.test": "shopping",
      "work-board.apps.example.test": "work-board"
    }
  }
}
```

Omit `public_ingress` to disable this listener. `socket_path` must be absolute,
at most 100 bytes, with an existing parent directory writable by the gateway.
The socket is mode 0660. A deployment-provisioned setgid parent directory can
grant a separate proxy group access. The gateway refuses existing files,
symlinks and active sockets, cleans up stale sockets after a crash, and removes
its socket on shutdown. Bind-mount only this directory into a proxy container;
do not bridge the ordinary loopback listener into it.

`hosts` is a nonempty exact map from public hostname to app **directory name**.
Hostnames may differ from private app hostnames: the original public Host is
preserved for browser origin checks and cookies. Unknown/unlisted hosts, the
index and admin return 404. Only `bubblewrap-hardened` process or CGI apps may be
routed; a weaker profile or static runtime returns 503. Process acquisition
rechecks the sandbox policy after waiting for startup or a draining restart,
and public requests use Unix backend ingress even during a manifest change.

This listener requires `auth.identity_header`, `auth.verify_url` and copying
the verifier's identity header. It strips edge-supplied identities, including
Remote-* and Remote_* headers, verifies every request even for `access: public`
apps and API paths, and checks manifest principal restrictions afterward.
A verifier bypass response without an identity fails closed. Forward-auth
metadata uses the actual request method, URI and host and fixes the scheme to
HTTPS. Forwarded client IPs still come from the trusted edge; do not use them
for authentication bypass policies. These are defenses against a compromised
edge forging identity or selecting other apps, not protection from that edge
reading valid browser cookies, stealing sessions or modifying app traffic.

`public_ingress.verify_url` optionally selects a separate HTTP(S) verifier for
public requests; it defaults to `auth.verify_url`. This supports a public
Authelia instance with independent sessions, storage and secrets. Private
requests always use `auth.verify_url`, regardless of forwarded host headers.
An app with `sandbox_options.network.forward_auth` also selects the public
verifier when its verification request's `X-Forwarded-Host` matches one of that
app's configured public aliases. Other hosts use the private verifier. Both
verifiers must return the same configured identity/group header names. The edge
must use the public verifier too; cookie names and session stores should differ.

The edge must provide HTTPS and trustworthy client IP metadata, strip incoming
identity headers, allowlist the same hosts and require Authelia on every app
path. Serve the login portal separately. Gateway configuration changes require
a restart; app manifests remain live. See [public ingress example](../examples/public-ingress.json).

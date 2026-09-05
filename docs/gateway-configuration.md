# Gateway configuration

Gateway configuration is loaded once at startup. Restart the gateway to apply changes; application manifests are polled separately. With no `-config` argument, the gateway reads `mini-cloud.json` from its working directory.

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
  "scan_interval": "1s",
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
| `scan_interval` | `1s` | Positive interval between directory scans. |
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

`scan_interval` controls recursive metadata polling. `default_idle` defaults to five minutes; zero disables idle shutdown. `ports` is an inclusive stable backend range and must begin above 1023.

## Authentication

`auth.identity_header` is the trusted authenticated identity and `auth.groups_header` is the comma-separated group list used by manifest `access_groups`; they default to `Remote-User` and `Remote-Groups`. When the identity header is absent and `verify_url` is configured, mini-cloud forwards the request headers and original request metadata to that authorization endpoint. A successful response must return the identity header; fields listed in `copy_headers` are added to the application request. Include the configured identity header in `copy_headers`; a successful verifier response without a copied identity produces 502. Include the configured groups header when any manifest uses group restrictions. Authorization redirects and failures are returned to the client without being followed. Without either a trusted identity header on the request or `verify_url`, an authenticated app returns 401. A verified identity outside the manifest allow lists receives 403. An empty identity-header setting disables authentication and manifest principal checks for local development. When authentication is enabled and `admin_identity` is nonempty, the admin host requires that exact trusted or verifier-supplied identity.

Because public and protected apps share the gateway, Caddy must not reject every anonymous wildcard request upfront. It should strip client-supplied identity headers and proxy to mini-cloud. Mini-cloud skips verification for public apps and calls `verify_url` for protected apps.

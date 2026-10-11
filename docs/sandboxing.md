# Hardened Bubblewrap

The Linux-only `bubblewrap-hardened` profile is opt-in. Existing `bubblewrap`
manifests keep their filesystem and network behavior. All process, CGI and cron
commands in a hardened app use the same policy; static serving remains outside
Bubblewrap.

## Gateway runtime

Provide only the runtime inputs needed by your apps:

```json
{
  "sandbox": {
    "runtime_paths": ["/run/current-system/sw"],
    "runtime_paths_file": "/nix/store/EXAMPLE-runtime-closure/store-paths",
    "egress_connection_limit": 128
  }
}
```

`runtime_paths` are explicit absolute read-only mounts. `runtime_paths_file`
contains one Nix store path per line, typically produced by `pkgs.closureInfo`.
At least one runtime path is required; the gateway never falls back to mounting
all of `/` or `/nix/store`. Include the closure of each executable and its
interpreter/shared libraries. The gateway executable is mounted separately and
must be static or have its dynamic dependencies in that closure. A system
profile directory alone supplies symlinks, not their store targets.

## App policy and defaults

```json
{
  "sandbox": "bubblewrap-hardened",
  "sandbox_options": {
    "writable_paths": ["data", "tmp"],
    "read_only_paths": ["/mnt/share/apps/chatgpt-client"],
    "network": {
      "allow_hosts": ["auth.openai.com", "api.openai.com"],
      "forward_auth": true,
      "max_connections": 32
    }
  }
}
```

The app's source directory is read-only. Only `data` and `tmp` are writable by
default. An explicit empty `writable_paths` list disables those mounts. Paths
must name app-relative directories; safe rooted opens reject traversal and
escaping symlinks and pinned descriptors prevent mount-source replacement.
`read_only_paths` defaults to empty and grants explicit absolute host inputs,
such as a shared library. These are deployment permissions: manifest authors
remain trusted. Keep runtime data out of source directories and ignore data and
caches in the app's watch configuration.

The app gets private user, PID, mount, IPC, UTS and network namespaces, a private
cgroup namespace where supported, minimal `/dev` and `/proc`, ephemeral `/tmp`
and `/var/tmp`, and an empty private home directory. Concrete CA files and `/etc/localtime` are
mounted read-only for TLS and the host timezone; the rest of `/etc` stays hidden. Capabilities and further
user namespaces are disabled. Host environment variables are not inherited;
explicit manifest and environment-file values are installed only inside the
namespace. They never reach Bubblewrap's host-side dynamic loader or its visible
command line. Hardened environment files
must stay within the app directory. HOME is `/home/mini-cloud`; Go caches default
to the app's `tmp` directory, CGO is disabled, and GOTOOLCHAIN is local. Source
runners need their dependency download hosts allowed on a cold cache.

## Ingress and egress

A process receives `LISTEN_NETWORK=unix` and
`LISTEN_ADDRESS=/run/mini-cloud/backend/app.sock`. It must listen on that Unix
socket, rather than treating PORT as a host TCP listener. The gateway uses the
socket for readiness and reverse proxying; symlinks are rejected and the socket
inode is pinned while connecting. PORT remains allocated for diagnostics and
compatibility but is not the hardened ingress. Existing unsandboxed and
`bubblewrap` apps continue to receive their host-loopback TCP address.

No host interfaces or host loopback are reachable in the private network
namespace. Standard HTTP(S) proxy variables point to a sandbox-local relay and
then to a filtered, per-app Unix proxy owned by the gateway. Clients must support
HTTP_PROXY/HTTPS_PROXY. Arbitrary sockets, DNS lookups inside the namespace,
UDP and protocols other than HTTP/HTTPS do not work.

Egress is denied by default. `allow_hosts` contains exact DNS hostnames; there
are no suffix wildcards. `"*"` permits any public DNS destination, useful for
link previews. HTTP uses port 80 and HTTPS CONNECT uses port 443. The proxy
resolves destinations itself, rejects every private/special answer and directly
attached host network, including globally routable IPv6, then pins an accepted
IP. Redirects make new proxy requests and undergo the same checks. CONNECT does
not inspect TLS or constrain URL paths: an allowed host is the permission
boundary. Shared public hosting and allowed remote proxies therefore remain
part of that trust decision. IPv4 literals are not accepted by exact allowlists;
private literals remain blocked even with `"*"`.

`forward_auth` defaults to false. When enabled, only GET
`http://auth.mini-cloud.internal/verify` with no query is forwarded to the
configured gateway `auth.verify_url`. This permits an app to verify browser
sessions without reaching other host services or the rest of Authelia's API.
When `public_ingress.verify_url` is configured, verification for this app's
public aliases (identified by `X-Forwarded-Host`) uses that endpoint instead.
Private hosts and other apps' aliases retain the private verifier.

Egress connections, including idle connections and CONNECT tunnels, are capped
at 32 per app (`max_connections`, configurable from 1 to 256) and 128 per gateway
(`egress_connection_limit`, configurable from 1 to 4096). Zero selects these
defaults. Excess connections are closed. CONNECT tunnels expire after five
minutes. Existing tunnels can survive allowlist tightening until the app closes
them or the tunnel expires. Changes to connection limits require a gateway
restart.

## Boundary and deployment

This profile contains a compromised app's filesystem and network access more
strongly than the legacy profile. It shares the host kernel and service UID,
and does not implement syscall filtering or CPU, memory, process or disk quotas.
The per-app data directory and explicitly granted credentials remain available
to that app. An exploited app can still consume or exfiltrate its own data to
allowed destinations. Other unsandboxed apps running as the same user remain
trusted and can affect hardened apps from outside their namespaces.

Deploy public ingress separately from the private wildcard proxy. Allowlist
specific app hosts and require authentication on every path, including API
routes that intentionally bypass browser authentication on the private network.
Hardened Bubblewrap does not change an app's `access` setting or make a public
routing/authentication decision.

Remote OAuth loopback callbacks are not host listeners in a private network
namespace. Use an app's supported manual callback flow, or implement a dedicated
callback relay. Do not add a general host-network exception for sign-in.

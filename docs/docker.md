# Docker and Podman

Run these commands from the repository root. Build the image with Podman (or substitute `docker` for the build command):

```sh
podman build -t mini-cloud .
```

## Local example with rootless Podman

Create a separate copy of the examples and a container-specific configuration. The repository's `mini-cloud.example.json` uses host paths and a loopback listener, so it cannot be mounted unchanged.

```sh
mkdir -p /tmp/mini-cloud-container
cp -R examples/apps /tmp/mini-cloud-container/apps
cat > /tmp/mini-cloud-container/config.json <<'JSON'
{
  "listen": "0.0.0.0:9080",
  "apps_dir": "/apps",
  "base_domain": "apps.test",
  "index_host": "apps.test",
  "admin_host": "admin.apps.test",
  "auth": { "identity_header": "" }
}
JSON
podman run --rm \
  --userns=keep-id:uid=10001,gid=10001 \
  --user 10001:10001 \
  -p 127.0.0.1:9080:9080 \
  -v /tmp/mini-cloud-container/apps:/apps:Z \
  -v /tmp/mini-cloud-container/config.json:/etc/mini-cloud.json:ro,Z \
  mini-cloud
```

The user-namespace mapping lets the container's UID/GID 10001 access files owned by your host user. `:Z` gives these mounts a private SELinux label. The example disables authentication and publishes only on host loopback.

In another terminal, verify all three runtimes and the index:

```sh
curl -fsS -H 'Host: hello.apps.test' http://127.0.0.1:9080/
curl -fsS -H 'Host: page.apps.test' http://127.0.0.1:9080/
curl -fsS -H 'Host: cgi.apps.test' http://127.0.0.1:9080/example
curl -fsS -H 'Host: apps.test' http://127.0.0.1:9080/
```

## Other container setups

For Docker or a different user-namespace arrangement, omit the Podman-specific `--userns` option and ensure the mounted apps directory is writable by the container's UID/GID 10001. Use a prepared copy or managed volume; changing ownership recursively on an existing checkout changes host permissions too. The configuration mount must be readable by that user.

The gateway writes generated agent guidance below `/apps` at startup and observes live edits there. Mount applications read-write. The image includes Python 3 and a shell for the examples; supply other trusted runtimes in a derived image. Backend process ports stay inside the container and do not need publishing.

For production, configure [authentication](gateway-configuration.md#authentication) and a TLS reverse proxy. A verifier address of `127.0.0.1` refers to the container itself; use an address reachable from the gateway container when the verifier runs elsewhere.

The image includes bubblewrap for apps that opt into `"sandbox": "bubblewrap"`. Nested user namespaces must be permitted by the container runtime and host; if they are blocked, sandboxed commands fail to start. The preset provides limited per-app process and write isolation, not resource or confidentiality isolation, and the container remains one trust domain. See the [security model](security.md#optional-bubblewrap-sandbox).

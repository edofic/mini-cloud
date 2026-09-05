# mini-cloud

mini-cloud is a small gateway for running mutable applications on one Linux or macOS machine. Put an application in a directory, edit it in place, and reach it through a hostname. HTTP processes start on demand and stop when idle; static files and CGI are also supported. On Linux, command-based apps can opt into a bubblewrap “containers lite” sandbox.

Each application directory contains a `mini-cloud.json` manifest. mini-cloud discovers it, serves static files or CGI, or starts an HTTP process on the first request. It watches the directory, drains requests before restarting changed processes, runs manifest cron jobs, and exposes a small app index and admin view.

When the gateway starts, it also installs generated `AGENTS.md` guidance and a `mini-cloud-app` skill in the applications directory. Agents can discover the local manifest format, URLs, paths, and project conventions without a separate setup step. The checked-in templates under [`templates/apps-root`](templates/apps-root) are authoritative.

## Quick start

Run these commands from the repository root on Linux or macOS. Enter `nix develop` for the included tools, or provide Go 1.27, Python 3, a POSIX shell, and curl yourself. Bubblewrap is also included in the Linux development shell. The example configuration disables authentication and listens only on loopback.

```sh
go run . -config mini-cloud.example.json
```

In another terminal:

```sh
curl -H 'Host: hello.apps.test' http://127.0.0.1:9080/
curl -H 'Host: page.apps.test' http://127.0.0.1:9080/
curl -H 'Host: cgi.apps.test' http://127.0.0.1:9080/example
curl -H 'Host: apps.test' http://127.0.0.1:9080/
curl -H 'Host: admin.apps.test' http://127.0.0.1:9080/
```

The `Host` headers select an app without DNS setup; requesting the listener by IP alone returns 404. For browser access, map `apps.test`, `admin.apps.test`, and the three app hostnames to `127.0.0.1` in your hosts file, then use port `9080`.

Edit `examples/apps/hello/server.py` or `examples/apps/hello/.env` and request it again. Static files are live immediately; a changed process is drained and restarted. Stop the gateway with Ctrl-C.

## How it works

The gateway watches immediate child directories of `apps_dir`. A process app receives a stable loopback port and `PORT`/`LISTEN_ADDRESS`; it starts on demand and stops after its idle timeout. CGI starts once per request. Static files are read from disk on every request. Invalid manifests leave the last valid configuration active and appear in the admin view.

The gateway is designed for one trusted Unix user. Put it behind a TLS/authentication proxy such as Caddy when serving protected or public applications. Its optional bubblewrap preset limits process visibility and writes but is not a confidentiality, resource, network, or tenant-isolation boundary. It does not provide full containers, builds, releases, rollback, backups, DNS, or distributed scheduling.

## Development

Use the reproducible development shell and run the same checks as CI:

```sh
nix develop
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
./scripts/test-examples.sh
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) and the [development guide](docs/development.md).

## Deployment

The flake exports the package, development shell, and `nixosModules.default`. The module creates one systemd service and a mutable apps directory; configure it with `services.mini-cloud.settings` and keep secrets outside the generated Nix-store JSON. See [NixOS setup](docs/nixos.md).

Native macOS builds and a sample launchd configuration are covered in the [macOS guide](docs/macos.md). macOS currently has a documented crash-cleanup limitation for child processes.

The [Docker guide](docs/docker.md) covers the unprivileged image, mounted applications, and runtime configuration. The image packages the gateway and example runtime tools; it is not an application-isolation boundary.

## Guides and examples

- [Detailed setup and use](docs/guide.md)
- [macOS setup](docs/macos.md)
- [Gateway configuration](docs/gateway-configuration.md)
- [Application manifests](docs/application-manifest.md)
- [Operations and lifecycle](docs/operations.md)
- [Security model](docs/security.md)
- [Architecture](docs/architecture.md)
- [Example applications](examples/apps)

## License

mini-cloud is available under the [Apache License 2.0](LICENSE).

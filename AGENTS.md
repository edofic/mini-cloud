# mini-cloud maintenance instructions

Keep the project self-contained and dependency-free unless a dependency materially simplifies a feature and is explicitly justified. The gateway is for one Linux machine and one trusted deployment user; do not expand it into a distributed orchestrator or release manager.

The application directory is authoritative and mutable. Never introduce gateway-managed releases, artifact copying, source control, builds, migrations, or rollback. Preserve the ability to live-edit files. Invalid manifests must leave the last valid configuration active and expose the error in the admin UI.

The gateway directly supervises application processes. Systemd may supervise the gateway as a single system service, but do not generate per-app units or timers. A gateway restart intentionally terminates all descendant apps; apps return on demand.

When behavior, configuration fields, defaults, limitations, security assumptions, lifecycle semantics, or examples change, update the relevant file in `docs/`, this README when appropriate, and example manifests in the same change. Treat documentation drift as a bug.

Run before handing off changes:

```sh
gofmt -w .
go test ./...
go vet ./...
./scripts/test-examples.sh
```

Use `go test -race ./...` where the installed Go distribution includes race runtime support. The Nix-provided Go toolchain on the original development host may report `package mini-cloud: cannot find package` because its race runtime is absent; do not mistake that toolchain limitation for a test failure.

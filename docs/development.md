# Development and testing

The project targets Linux and macOS with Go 1.27 and uses only the standard library. The examples and integration tests also need Python 3, a POSIX shell, curl, and standard Unix utilities. Race tests need a C compiler and a Go distribution with the race runtime. Bubblewrap tests run only on Linux.

The Nix flake supplies the development tools:

```sh
nix develop
```

Without Nix, install Go 1.27 and golangci-lint 2.13.2 alongside those test prerequisites. Run the same checks used by GitHub Actions:

```sh
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
golangci-lint run
./scripts/test-examples.sh
```

Integration tests start a real HTTP child, proxy through it, edit its manifest, verify drain/restart, and wait for idle shutdown. Other tests exercise CGI parsing, last-valid configuration retention, stable ports, and cron expressions. The example smoke test runs all three runtime types.

For lifecycle changes, also consider simultaneous cold requests, an edit during an active request, startup exit, idle/new-request races, SIGTERM with descendants, WebSocket drains, and cron overlap policies.

CI checks formatting without changing files and runs the tests, race detector, vet, and example smoke tests on Linux and macOS. Linux CI also runs the linter, Bubblewrap test, and Docker build. The linter uses its standard checks; fix reported issues rather than disabling checks globally.

If a Nix-provided Go distribution reports `package mini-cloud: cannot find package` only with `-race`, check whether its race runtime is present. CI requires race tests on the official Linux Go distribution.

The smoke test copies examples to a temporary directory and removes it afterward. It listens on port 19080 by default; select another port if needed:

```sh
MINI_CLOUD_TEST_PORT=19081 ./scripts/test-examples.sh
```

When changing behavior or configuration, update the relevant documentation and example manifests together. Generated application guidance comes from [`templates/apps-root`](../templates/apps-root); edit those templates rather than generated files under an apps directory.

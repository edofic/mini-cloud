# Contributing

mini-cloud is a small Linux and macOS gateway for one machine and one trusted deployment user. Keep it self-contained and use the Go standard library unless a dependency materially simplifies a feature and its benefit is explicitly justified.

Start with the [development guide](docs/development.md). Include a small reproduction and sanitized configuration when reporting bugs. For changes, explain the problem, resulting behavior, and relevant validation in the pull request. Update the relevant documentation and example manifests whenever behavior, configuration, defaults, or lifecycle semantics change.

Application files stay mutable and authoritative. The gateway supervises application processes directly; application builds, releases, migrations, rollback, distributed orchestration, and per-app systemd units are outside its scope.

The [Apache License 2.0](LICENSE) covers the code, documentation, examples, and embedded templates. Contributions are submitted under the same license.

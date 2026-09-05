# NixOS setup

The flake exports a package, `nix develop`, and `nixosModules.default`. Add the repository as a flake input and import the module:

```nix
inputs.mini-cloud.url = "github:edofic/mini-cloud";

modules = [
  inputs.mini-cloud.nixosModules.default
  ({ pkgs, ... }: {
    services.mini-cloud = {
      enable = true;
      appsDir = "/var/lib/mini-cloud/apps";
      runtimePackages = [ pkgs.bash pkgs.coreutils pkgs.python3 ];
      settings = {
        base_domain = "apps.example.test";
        index_host = "apps.example.test";
        admin_host = "admin.apps.example.test";
        auth = {
          admin_identity = "operator";
          verify_url = "http://127.0.0.1:9091/api/authz/forward-auth";
        };
      };
    };
  })
];
```

The module creates one `mini-cloud` system user/group by default, creates the mutable home and apps directories, installs the gateway as one systemd service, puts configured runtime packages on `PATH`, and uses `KillMode=control-group`. Override `user`, `group`, `home`, `runtimePackages`, `environment`, `package`, and `settings` when integrating with an existing host.

`settings` becomes a JSON file in the Nix store. Do not put passwords, tokens, or other secrets there; use an environment file in the protected apps directory or a separate secret mechanism. Caddy, Authelia, DNS, TLS, and host-specific virtual hosts remain outside this module.

Build the package with `nix build .#mini-cloud`. The default development shell includes Go 1.27, golangci-lint, Python, curl, shell tools, and a compiler for race tests.

The example assumes an authorization service at the configured URL and a trusted edge proxy; the module does not create either. See [gateway authentication](gateway-configuration.md#authentication). Without a verifier or an identity supplied by that proxy, protected apps, the index, and admin return 401.

`appsDir` overrides any `settings.apps_dir`. The service working directory and `HOME` default to `/var/lib/mini-cloud`; relative configuration paths resolve there. The default runtime packages are Bash, coreutils, and Python 3. Setting `runtimePackages` replaces that list. If you select a different user or group, declare that account yourself.

Application secrets can be loaded with manifest `environment_files`. Values placed in the module's `environment` option also become part of the Nix configuration; keep secrets out of that option as well. For service-wide secrets, use a separately managed systemd `EnvironmentFile`.

After applying your NixOS configuration, inspect the service and logs:

```sh
systemctl status mini-cloud.service
journalctl -u mini-cloud.service -f
```

Deploy mutable app files as the configured service user, with write access to the apps root for generated guidance. A service restart terminates all apps; HTTP processes return on demand.

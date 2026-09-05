# Application manifests

An app is one immediate directory below `apps_dir` containing `mini-cloud.json`. Its directory name is its runtime identity and stable-port key.

Each manifest must contain exactly one of `process`, `static`, or `cgi`. The common-fields fragment below must be combined with one runtime block. Durations are JSON strings such as `"30s"`; `idle` inherits the gateway default, and `"0s"` disables process idle shutdown.

Common fields:

```json
{
  "host": "optional.example.test",
  "display_name": "Human-friendly name",
  "description": "Short text for the app directory",
  "icon": "assets/icon.svg",
  "access": "authenticated",
  "access_users": ["alice"],
  "access_groups": ["admin"],
  "sandbox": "bubblewrap",
  "idle": "5m",
  "watch": { "ignore": ["data", "tmp/*"] },
  "cron": []
}
```

`display_name`, `description`, and `icon` customize the lightweight app directory. `icon` is an app-relative image path served directly by mini-cloud; it does not wake a stopped process. Its resolved path must remain within the app directory. `access` is `public` or `authenticated` and defaults to authenticated.

`sandbox` is optional. Set it to `"bubblewrap"` to run every command belonging to the app—process, CGI, and cron—inside the built-in bubblewrap preset. Omit it to execute commands directly as before. Static file serving is performed by the gateway and is not sandboxed, though cron commands on a static app are.

The preset gives commands private user, PID, IPC, UTS, mount, `/tmp`, `/proc`, and `/dev` namespaces, plus a private cgroup namespace where the host supports one. The app directory stays writable at its existing absolute path, while the rest of the host filesystem is visible read-only so installed runtimes and libraries continue to work. Networking is shared with the host because process apps must bind the gateway-assigned loopback port. Bubblewrap must be installed as `bwrap` on the gateway `PATH`, and the host must permit unprivileged user namespaces. Failure to create the sandbox is reported as a normal process/CGI/cron start error.

This is lightweight damage containment, not a confidentiality or multi-tenant boundary: commands can read files visible to the gateway user, use the host network, consume unbounded resources, and cooperate through the writable app directory. See the [security model](security.md#optional-bubblewrap-sandbox).

For an authenticated app, `access_users` and `access_groups` optionally restrict access to the listed identities and groups. The lists are combined with OR semantics: the example permits user `alice` and every member of group `admin`. With neither list, any authenticated identity is accepted for backward compatibility. Principal matching is exact and case-sensitive. Public apps cannot specify either list. The app directory uses these same rules and omits apps the current identity cannot access.

Watch patterns are relative to the app root and use shell-style path matching: `*` does not cross `/`, and `**` has no special recursive meaning. Matching directories are excluded recursively. Top-level `.git`, `.hg`, and `.svn` directories are always ignored. Manifest and `.env` changes otherwise restart a running process. Ignore databases, caches, and generated output to prevent restart loops. See [live editing limitations](operations.md#live-editing).

## Process

```json
{
  "process": {
    "command": ["./server", "--addr", "${LISTEN_ADDRESS}"],
    "working_directory": ".",
    "environment": { "MODE": "home" },
    "environment_files": [".env"],
    "startup_timeout": "30s",
    "shutdown_timeout": "10s",
    "readiness": { "type": "http", "path": "/health" },
    "restart": {
      "policy": "on-failure",
      "delay": "2s",
      "maximum_attempts": 5
    }
  }
}
```

Commands are argument arrays without an implicit shell. `working_directory` defaults to the app root. Relative executable paths such as `./server` resolve from that working directory; bare commands such as `python3` are found on the gateway's `PATH`. Environment-file paths resolve from the app root. Absolute paths are allowed for these trusted commands and files.

Environment precedence is gateway environment, environment files in order, manifest values, then gateway fields `APP_NAME`, `PORT`, and `LISTEN_ADDRESS`. Environment files must be listed explicitly; `.env` is not loaded automatically. They support `KEY=VALUE`, whole-line `#` comments, and matching outer quotes, without shell evaluation. Command arguments expand `$NAME` and `${NAME}` using the resulting environment; missing variables become empty strings.

Startup and shutdown timeouts default to `30s` and `10s`. Readiness defaults to `tcp`; HTTP readiness defaults to path `/` and accepts 2xx or 3xx without following redirects. The app must bind `127.0.0.1:$PORT`. Shutdown sends SIGTERM to the process group and SIGKILL after the timeout; wrappers should use `exec`.

Restart defaults are `on-failure`, a `2s` delay, and five automatic attempts. Zero values for process timeouts, restart delay, and attempt count select these defaults. `never` disables automatic retries; a later request can still start a stopped or failed process. **Current limitation:** `always` passes manifest validation but does not trigger automatic retries. Use `on-failure` when automatic recovery is needed. See [failure recovery](operations.md#failure-recovery).

## Static

```json
{
  "static": {
    "root": "public",
    "spa_fallback": "index.html",
    "browse": false
  }
}
```

Files are read live. Set `root` explicitly to a directory containing only public assets: an omitted root serves from the app directory. Directories serve `index.html` when present. SPA fallback is optional and browsing defaults off. Files and followed symlinks must resolve inside `root`; escaping symlinks are rejected.

## CGI

```json
{
  "cgi": {
    "command": ["./handler"],
    "working_directory": ".",
    "environment_files": [".env"],
    "timeout": "30s",
    "maximum_concurrency": 4
  }
}
```

CGI also accepts an `environment` map and uses the same command, working-directory, and environment-file rules as process apps. Timeout defaults to `30s` and maximum concurrency to `4`; zero selects those defaults. Excess requests wait for a slot, and the execution timeout begins after acquiring it.

One process handles one request. The body is stdin, RFC CGI variables describe the request, and stdout contains MIME headers, a blank line, then the response body. `Status: 201 Created` selects a status. stderr goes to the journal. CGI is unsuitable for WebSockets and connection pooling.

## Cron

```json
{
  "cron": [{
    "name": "cleanup",
    "schedule": "15 3 * * *",
    "command": ["./app", "cleanup"],
    "environment": { "MODE": "cleanup" },
    "timeout": "30m",
    "overlap": "forbid",
    "missed": "skip"
  }]
}
```

Five numeric fields represent minute (0–59), hour (0–23), day of month (1–31), month (1–12), and weekday (0–6, Sunday is 0). `*`, comma-separated lists, ranges, and steps such as `*/5` work. Names, macros such as `@daily`, and Sunday as 7 are unsupported. **All five fields must match**, including both day of month and weekday. The gateway's local timezone is used, with due jobs checked every five seconds.

Each job requires a unique nonempty `name`, a `schedule`, and a nonempty `command`. Timeout defaults to `1h`, including when set to `"0s"`. Job `environment` values override inherited manifest values; the gateway supplies `APP_NAME` and `CRON_JOB`, but does not add a backend port for jobs. Static-app jobs use the app root as their working directory.

Overlap is `forbid` (default, skip while a prior invocation runs), `allow` (run concurrently), or `replace` (cancel prior invocations and start another). Only `missed: "skip"` is supported; downtime is never replayed. Removing a job cancels its running invocation. Jobs inherit runtime working directory and environment, but do not start the HTTP process or postpone its idle shutdown.

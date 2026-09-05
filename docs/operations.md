# Operations and lifecycle

## Live editing

The scanner fingerprints path, size, modification time, and mode for every non-ignored file. A change to a running process triggers drain and restart. A stopped process stays stopped and consumes the changes on its next activation. Static and CGI files take effect on their next request. A long-lived WebSocket prevents a drain from completing until it closes.

Polling compares metadata, not file contents, so an edit that preserves size, modification time, and mode may be missed. The scanner does not follow symlinks: changing a symlink is detected, but edits only to its target are not. Symlinked directories directly under `apps_dir` are not discovered as apps.

Invalid manifest edits keep the last valid runtime configuration. This does not restore changed source files or data. A new app with an invalid manifest appears in admin but has no route until a valid configuration is accepted. Fix the file and a later scan applies it.

Removing the app directory or its manifest unregisters the app, stops its process, and cancels its cron invocations. Use an atomic file replacement when saving manifests to avoid a temporary absence being treated as removal.

## Failure recovery

With `on-failure`, unexpected failed exits trigger delayed retries up to `maximum_attempts`. A clean exit after readiness is not retried. Failed command setup, an occupied backend port, or a readiness timeout does not itself schedule the same automatic exit retry. A later request can attempt another start, resetting the retry budget; an accepted file change also resets it.

The parser accepts `always`, but the supervisor currently schedules automatic retries only for `on-failure`. Idle stops, file-change restarts, and shutdown are intentional stops and do not use that retry policy.

## Logs and admin

The gateway logger writes events and captured child stdout/stderr to stderr. CGI stdout is the HTTP response; its stderr is logged. Under systemd:

```sh
journalctl -u mini-cloud.service -f
```

Entries include fields such as `app=hello`, `job=cleanup`, `stream=stderr`, and `event=ready`. The gateway does not store or query historical logs.

The configured admin host shows runtime type, access, state, port, active requests, restart count, cron results, process errors, and manifest errors. `/api/apps` returns the same snapshot as JSON. The page is intentionally observational: it has no deploy or remote process-control actions.

## Shutdown

SIGTERM stops HTTP and terminates child process groups. Production systemd configuration should perform cgroup-wide cleanup after crashes. On restart, process apps begin stopped and return on demand; static and CGI apps work immediately.

## Deployment remains external

Edit files directly, use Git in the directory, or point a command through your own `current` symlink (subject to the scanning limitations above). The gateway observes changes but owns none of those strategies.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| 404 with `unknown mini-cloud application` | Send the configured hostname in `Host`; check manifest validity, discovery, and host collisions in admin. |
| 401 on an authenticated app | Supply identity through the trusted proxy or configure a verifier. The default config has no verifier. |
| 403 | Check exact user/group allow lists or `admin_identity`. |
| 502 during authentication | Check verifier reachability and that `copy_headers` includes the configured identity header. |
| 503 starting a process | Inspect the admin error and logs for command, environment-file, port, or readiness failures. |
| Process repeatedly restarts after writing data | Add output directories to `watch.ignore`. |
| File edit does not restart a process | Check polling delay, ignored paths, symlink targets, and active requests delaying drain. |
| Startup fails while generating guidance | Make `apps_dir` writable by the gateway user. |

Back up mutable application files and data using external tools. Restarting or upgrading the gateway does not preserve running application processes.

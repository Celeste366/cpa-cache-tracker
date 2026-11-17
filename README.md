# CPA Cache Tracker

A native CLIProxyAPI v8 cache telemetry plugin, with a local management panel.

## Open the panel

http://127.0.0.1:8080/v0/resource/plugins/cpa-cache-tracker/panel

The panel uses the management center's saved management key when available. In a fresh browser session, enter the management key using the connection dialog. Do not enter the bcrypt hash from config.yaml. The plugin does not make unauthenticated polling requests, and stops polling on HTTP 401/403 until you reconnect. An existing CPA IP ban must expire before reconnecting.

## Build and install

Requires Go 1.26+, a C compiler (GCC/Clang on Unix; MinGW-w64 on Windows for `-buildmode=c-shared`), and Node.js for frontend tests.

Supports multi-OS/multi-architecture builds (`darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`, `windows/amd64`, `windows/arm64`). `Makefile` detects `GOOS`/`GOARCH` automatically (or accepts `GOOS=... GOARCH=...` overrides) and outputs the host-matching library extension (`.dylib`, `.so`, or `.dll`) into `bin/`, installing to `~/.cli-proxy-api/plugins/<os>/<arch>/`:

```sh
make test
go vet ./...
make build
make install
```

On Linux containers/hosts, compile against the same libc family as the CPA binary (`glibc` vs `musl`). On cross-compilation hosts, pass the target C compiler via `CC=...` alongside `GOOS`/`GOARCH`.

Release: **0.1.4**, C ABI 1, schema 10. The host switches native libraries by versioned path, so replacing a loaded library at the same path does not activate its new bytes. Install a new versioned filename and trigger a CPA config reload. Keep the previous version or backup for rollback.

Enable under CPA config.yaml:

```yaml
plugins:
  enabled: true
  configs:
    cpa-cache-tracker:
      enabled: true
      trace_enabled: true
      retention_days: 14
      max_disk_mb: 200
      max_message_hashes: 512
      data_dir: .cache_tracker_data
```

Merge this block into the existing configuration; do not replace other plugins. Both `~/...` and Windows `~\...` prefixes expand to the user's home directory; when the host runs with a root or Windows system working directory (`/` or `C:\Windows\System32`), relative `data_dir` values resolve under `~/.cli-proxy-api/` (or `%USERPROFILE%\.cli-proxy-api\` on Windows). Fingerprints retain hashes, lengths and routing/session identifiers; prompt text, tool schemas and API keys are not persisted.

## Accounting and limits

- Token hit rate is cache-read tokens divided by effective input tokens. Native Claude uses independent cache buckets; OpenAI-compatible providers, WorkBuddy and other inclusive providers use subset accounting.
- Default KPIs use successful main generation calls with positive effective input. Failed calls, compaction, internal callbacks and non-generation events remain queryable separately.
- Miss labels are local diagnostic indications, not proof of the upstream cache's eviction cause. Fingerprints capture the pre-executor payload; later executor rewrites are not visible.
- Matching prefers a shared host selection_id, then auth/model and FIFO within a trace. Nested same-auth/model calls without a selection_id remain ambiguous.
- Full recent records are capped at 500 in an LRU. Older diffs and session predecessors load from retained JSONL. Compact indexes scale with retained records; retention bounds disk by age and size.
- Retention is checked at startup, config changes, periodically, and every 100 appends. The disk limit can be temporarily exceeded between checks.
- Turning tracing off hot-reconfigures body omission; token collection and lightweight routing metadata remain active.

Authenticated APIs live under /v0/management/plugins/cpa-cache-tracker/: status, summary, records, records/diff and purge. Configuration uses CPA's native config endpoint. Purge deletes tracker records only and cannot be undone.

## Release 0.1.1

Preserves usage session IDs when interception metadata is missing; handles WorkBuddy's inclusive input accounting; excludes non-generation and zero-input events from main hit rates; recognizes actual host callback metadata; prefers selection IDs for nested calls; bounds retained full fingerprints; stops credential-error polling; waits for runtime configuration synchronization before reporting reload success.

## Release 0.1.2

Forest/Porcelain dashboard redesign: responsive sidebar, clearer overview and metric hierarchy, paired trend/diagnosis panels, full-width model/request tables, consistent settings and diff surfaces, accessible filter/navigation labels, and reduced-motion support. Existing API paths, data bindings, tracing controls and authentication behavior are retained.


## Release 0.1.3

Embeds the dashboard favicon to avoid a missing host-level /favicon.ico request. No external assets are required.

## Release 0.1.4

Follows the system light/dark appearance automatically using prefers-color-scheme, including controls, tables, badges, settings, authentication and diff panels. Canvas charts read shared CSS palette tokens and redraw immediately on a system appearance change without reloading or waiting for a data refresh. No stored theme override is applied.

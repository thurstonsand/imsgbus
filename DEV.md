# DEV.md

## Setup

```sh
mise trust && mise bootstrap
```

## Commands

If `mise` is unavailable, inform the user. Always prefer these tasks over running anything directly.

```bash
mise run build          # Build binary to bin/imsgbus
mise run test           # Run unit tests
mise run lint           # Every lint:* check at once
mise run lint:go        # Or one: lint:versions, lint:go, lint:actions, lint:markdown, lint:changelog, lint:renovate
mise run fix            # Auto-fix lint and formatting issues
mise run imsgbus -- [ARGS]
mise run update-deps    # Upgrade Go modules + pinned tools, refresh lockfiles
mise run release-check  # Build snapshot release artifacts
mise run release X.Y.Z  # Release [Unreleased] as X.Y.Z: commit, tag, push
mise run clean          # Remove build artifacts
```

`GOFLAGS=-tags=goolm,sqlite_fts5` is set in `mise.toml`, so builds use mautrix's pure-Go olm instead of libolm and compile SQLite's FTS5 for the search index. Builds need cgo for mattn/go-sqlite3, which mxmain imports, so CI and releases run on macOS.

## Layout

- `main.go`: the mxmain host process.
- `internal/imsg`: the JSON-RPC client for one `imsg rpc` child, and typed wrappers for the methods imsgbus calls.
- `internal/contacts`: the contacts catalog, read from the AddressBook stores: names for handles, and handles for names.
- `internal/search`: the pluggable search `Backend` and its FTS5 implementation.
- `internal/core`: frontend-neutral service over imsg.
- `internal/matrix`: the bridgev2 network connector.

## Code style

- Pass imsg the parameters it documents and nothing else: it rejects unknown ones.
- A message GUID imsgbus creates is a lowercase UUID, passed to `send.tracked` as `attempt_id`, so a Matrix event's transaction and the echo from the watch feed share it.

## Changelog

A user-facing change adds its entry under `## [Unreleased]` in `CHANGELOG.md`. Released sections are `## [X.Y.Z] - YYYY-MM-DD`, newest first; `mise run lint:changelog` enforces the format and that the latest release is `main.go`'s `Version`.

# PS3 Game Manager

PS3 Game Manager (`ps3mgr`) is a low-configuration Go application for browsing a local PS3 game library, discovering a PS3 on a private network, comparing installed games, and uploading missing games through a sequential transfer queue.

It replaces the external `bash`, `nmap`, `timeout`, and `lftp` dependencies used by `ps3-games.sh`. The executable contains its FTP client, network scanner, HTTP API, SSE event stream, and web interface.

## Build and run

Go 1.23 or newer is required.

```sh
go build -o ps3mgr ./cmd/ps3mgr

export PS3MGR_GAME_DIR=/data/games
./ps3mgr serve
```

Open `http://127.0.0.1:8080`. The server binds to localhost by default so it is not accidentally exposed to the LAN.

## Docker

Build and run the image locally:

```sh
docker build \
  --build-arg VERSION=dev \
  -t ps3mgr:dev .

docker run --rm \
  -p 8080:8080 \
  -v /data/games:/games:ro \
  ps3mgr:dev
```

The container listens on `0.0.0.0:8080` and reads games from `/games`. All other environment variables work normally with `docker run -e`.

On Linux, host networking can make local PS3 discovery more predictable:

```sh
docker run --rm \
  --network host \
  -v /data/games:/games:ro \
  ghcr.io/OWNER/ps3mgr:latest
```

Replace `OWNER` with the lowercase GitHub account or organization name.

## Automated releases and GHCR

The GitHub Actions workflow performs formatting, vet, race tests, and builds before publishing anything.

- Every pushed commit publishes a multi-architecture image as `ghcr.io/OWNER/REPOSITORY:sha-<full-commit-sha>`.
- A push to the repository's default branch also updates `:latest`.
- A tag such as `v1.2.3` publishes container tags `1.2.3`, `1.2`, and `1`, then creates the corresponding GitHub Release.
- Version tags containing a suffix, such as `v1.2.3-rc.1`, create a prerelease and do not replace the latest stable GitHub Release.
- Release assets cover Linux, macOS, and Windows on amd64 and arm64, accompanied by `checksums.txt`.

Create a release by pushing a semantic version tag:

```sh
git tag v1.0.0
git push origin v1.0.0
```

GHCR publishing uses the workflow's built-in `GITHUB_TOKEN`; no registry password secret is required. Repository Actions must be allowed to write packages.

For development:

```sh
go fmt ./...
go vet ./...
go test ./...
go build ./...
```

## Local library layout

Each immediate child directory of `PS3MGR_GAME_DIR` is treated as one game:

```text
/data/games/
├── Battlefield 3/
│   └── PS3_GAME/
│       ├── PARAM.SFO
│       └── ICON0.PNG
├── Dead Space 3/
└── PES 2013/
```

When available, `PS3_GAME/PARAM.SFO` supplies the title, title ID, version, and inferred region, while `ICON0.PNG` supplies the cover. A malformed or incomplete game directory remains visible using its directory name and the built-in placeholder.

## Commands

```text
ps3mgr --help
ps3mgr local-games [--dir /data/games] [--json]
ps3mgr scan 192.168.1.0/24 [--workers 32] [--json]
ps3mgr consoles [--json]
ps3mgr games --ip 192.168.1.152 [--remote-dir /dev_hdd0/GAMES] [--json]
ps3mgr compare --ip 192.168.1.152 [--dir /data/games] [--json]
ps3mgr install --ip 192.168.1.152 "PES 2013" "Dead Space 3"
ps3mgr install --ip 192.168.1.152 --stop-on-error "PES 2013"
ps3mgr serve [--listen 127.0.0.1:8080] [--dir /data/games]
```

Game arguments accepted by `install` can be an exact title, title ID, or the stable ID shown by `local-games --json`. Flags take precedence over environment defaults.

`consoles` reports in-memory discoveries from the running application. The web panel maintains this state for its lifetime; no database or configuration file is created.

## Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `PS3MGR_GAME_DIR` | `.` | Local directory containing game folders |
| `PS3MGR_REMOTE_GAME_DIR` | `/dev_hdd0/GAMES` | PS3 destination/library directory |
| `PS3MGR_LISTEN` | `127.0.0.1:8080` | Web listen address |
| `PS3_FTP_USER` | `anonymous` | FTP username |
| `PS3_FTP_PASSWORD` | empty | FTP password |
| `PS3MGR_WORKERS` | based on CPU, maximum 32 | Concurrent network scan workers |
| `PS3MGR_FTP_TIMEOUT` | `8s` | FTP operation and connection timeout |

Durations use Go syntax such as `500ms`, `8s`, or `2m`.

## PS3 and network requirements

- Enable an FTP server on the PS3 (commonly through webMAN MOD, multiMAN, or another trusted homebrew environment).
- Ensure the computer and PS3 can reach one another on the local network and TCP port 21 is permitted.
- Use only a private/local IPv4 CIDR that you are authorized to administer. Public networks and ranges larger than `/16` are rejected.
- The detector confirms PS3 filesystem markers such as `/dev_hdd0` or `/dev_flash`; an arbitrary FTP server is not reported as a PS3.
- Configure credentials if the FTP server does not allow anonymous access.

The FTP client supports passive mode (EPSV with PASV fallback), binary transfers, directory creation, per-file retries, reconnection, `REST`-based resume where supported, and cancellation. A completed remote file is skipped during a resumed installation.

## Transfer behavior

All callers use one application-level queue. Games transfer one at a time. A failed or cancelled game does not stop later games by default; use `--stop-on-error` in the CLI or the corresponding API field to cancel the remainder of that submitted queue.

The web panel provides:

- searchable, sortable, filterable game cards and cover images;
- select all, select missing, and selection size totals;
- private CIDR discovery and console selection;
- installed/missing comparison using title ID first and normalized title second;
- live SSE progress, speed, ETA, current file, retry, cancellation, pause-after-current, and clear-completed controls;
- in-page completion/error toasts and optional browser notifications.

`Pause` prevents the next game from starting; it does not interrupt the active FTP upload. `Cancel` uses context cancellation for the active game. The queue then continues to the next game unless stop-on-error was requested.

## HTTP API

The embedded web UI uses the same public application services as the CLI:

```text
GET    /api/health
GET    /api/local-games
GET    /api/local-games/{id}/icon
POST   /api/scan
GET    /api/consoles
GET    /api/consoles/{id}
GET    /api/consoles/{id}/games
GET    /api/compare/{id}
POST   /api/queue
GET    /api/queue
GET    /api/queue/{id}
POST   /api/queue/{id}/cancel
POST   /api/queue/{id}/retry
POST   /api/queue/pause
POST   /api/queue/resume
DELETE /api/queue/completed
GET    /api/events
```

Example scan request:

```sh
curl -X POST http://127.0.0.1:8080/api/scan \
  -H 'Content-Type: application/json' \
  -d '{"cidr":"192.168.1.0/24","workers":32}'
```

Example queue request:

```json
{
  "console_id": "192.168.1.152",
  "game_ids": ["BLES01717", "deadspace3"],
  "stop_on_error": false
}
```

Requests are size-limited and validated. The API does not accept arbitrary FTP commands or local filesystem paths.

## Troubleshooting

**The local library does not load**

Check that `PS3MGR_GAME_DIR` exists, is a directory, and is readable by the current user. Run `ps3mgr local-games` to see the contextual filesystem error directly.

**The scan finds nothing**

Confirm the CIDR from the computer's network settings, verify that FTP is enabled on the PS3, and try `ps3mgr games --ip <PS3-IP>` to test the known address directly. Discovery deliberately ignores generic FTP servers.

**Login fails**

Set `PS3_FTP_USER` and `PS3_FTP_PASSWORD` to the credentials configured by the PS3 FTP server. Passwords are neither logged nor returned through the API.

**A large upload was interrupted**

Retry the queue item. Files already complete on the PS3 are skipped, and partial files resume when the FTP server supports `SIZE` and `REST`. If the server rejects resume, the affected file—not the whole game—is sent again.

**The browser cannot connect from another device**

The safe default is loopback-only. To intentionally allow LAN access, choose a specific LAN interface address, for example `--listen 192.168.1.20:8080`. Avoid `0.0.0.0` unless that exposure is intended and protected by the local firewall.

**Go reports `error obtaining VCS status` while building**

This can happen when the source directory contains an empty or damaged `.git` directory. Repair/remove that repository metadata, or build without embedding VCS information:

```sh
go build -buildvcs=false -o ps3mgr ./cmd/ps3mgr
```

## Project structure

```text
cmd/ps3mgr/          executable and signal handling
internal/app/        shared application orchestration
internal/cli/        command interface
internal/config/     environment defaults
internal/domain/     game, console, event, and transfer models
internal/events/     in-memory event bus
internal/ftp/        native FTP protocol and PS3 operations
internal/games/      filesystem library, SFO parser, matching
internal/scanner/    bounded private-CIDR scanner
internal/transfers/  sequential queue state machine
internal/web/        HTTP API, SSE, and embedded UI
```

Runtime state intentionally remains in memory. The local filesystem and PS3 are the sources of truth.

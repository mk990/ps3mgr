# PlayStation Manager

PlayStation Manager (`ps3mgr`) manages PS2 games through Open PS2 Loader USB storage, PS3 games through FTP, and PS5 games through etaHEN/ShadowMountPlus FTP. Every platform has isolated library, device, event, and queue state, so work on one platform never blocks another platform.

It replaces the external `bash`, `nmap`, `timeout`, and `lftp` dependencies used by `ps3-games.sh`. The executable contains its FTP client, network scanner, HTTP API, SSE event stream, and web interface.

## Build and run

Go 1.23 or newer is required.

```sh
go build -o ps3mgr ./cmd/ps3mgr

export PS3MGR_GAME_DIR=/data/games
export PS3MGR_PS2_GAME_DIR=/data/ps2
export PS3MGR_PS2_SYSTEM_DIR=/data/ps2/system
export PS3MGR_PS2_USB_MOUNT_ROOT=/mnt/usb
export PS3MGR_PS2_COVER_DOWNLOAD=true
export PS3MGR_PS5_GAME_DIR=/data/ps5
export PS3MGR_PS5_REMOTE_GAME_DIR=/data/etaHEN/games
export PS3MGR_PS5_FTP_PORT=2121
./ps3mgr serve
```

Open `http://127.0.0.1:8080`. The server binds to localhost by default so it is not accidentally exposed to the LAN.

Platform pages have stable, refresh-safe paths: `/ps2-games`, `/ps2-usb`, `/ps2-queue`, `/ps3-games`, `/ps3-consoles`, `/ps3-scan`, `/ps3-queue`, `/ps5-games`, `/ps5-consoles`, `/ps5-scan`, and `/ps5-queue` (with `/dashboard` for the overview).

## Docker

Build and run the image locally:

```sh
docker build \
  --build-arg VERSION=dev \
  -t ps3mgr:dev .

docker run --rm \
  -p 8080:8080 \
  -v /data/games:/games:ro \
	-v /data/ps2:/data/ps2 \
	-v /data/ps5:/data/ps5:ro \
	-v /media/ps2-usb:/mnt/usb \
  ps3mgr:dev
```

The USB bind mount supports both common layouts. A USB can be bound directly (`-v /media/usb0:/mnt/usb`) and appears as `usb-root`, or a parent can contain several targets (`/mnt/usb/usb0`, `/mnt/usb/usb1`). Standard OPL folders such as `DVD`, `ART`, and `CFG` are never mistaken for devices. The application does not mount devices, access `/dev/sdX`, or require privileged mode. It only writes to discovered targets inside `PS3MGR_PS2_USB_MOUNT_ROOT`; ensure the container user can read and write those host mounts. The PS2 library or at least its `covers` subdirectory must be writable for cover caching. To keep ISOs read-only, bind `/data/ps2` read-only and add a second writable bind at `/data/ps2/covers`.

The `/ps2-usb` page reports the mounted filesystem and FAT32 compatibility as `COMPATIBLE`, `INCOMPATIBLE`, or `UNKNOWN`. Linux detects FAT-family, exFAT, ext, XFS, Btrfs, tmpfs, and FUSE-backed mounts through filesystem metadata. A Docker bind mount cannot expose the MBR partition table and FAT metadata does not reliably distinguish FAT16 from FAT32, so verify ambiguous targets on the host. Formatting and partitioning remain host responsibilities.

`Initialize OPL Layout` is Docker-safe: it accepts a discovered USB target ID, creates the OPL directories, and copies `PS3MGR_PS2_SYSTEM_DIR` into that mounted target. It never formats, partitions, mounts, or unmounts a device.

If `/ps2-usb` shows no target, inspect `GET /api/ps2/usb/status`. It reports the exact configured root, discovery mode, and inaccessible/skipped paths. After changing a Docker bind mount, recreate the container—Docker cannot add a new host bind mount to an already-created container. A direct bind to `/mnt/usb` appears as `usb-root`; a parent layout exposes child IDs such as `usb0`.

The container listens on `0.0.0.0:8080` and reads games from `/games`. All other environment variables work normally with `docker run -e`.

On Linux, host networking can make local PS3 discovery more predictable:

```sh
docker run --rm \
  --network host \
  -v /data/games:/games:ro \
	-v /data/ps2:/data/ps2:ro \
	-v /data/ps2-covers:/data/ps2/covers \
	-v /data/ps5:/data/ps5:ro \
	-v /media/ps2-usb:/mnt/usb \
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

### Offline PS2 covers

PS2 covers are cached local files. When a scanned game has a known serial and no matching local cover, the server downloads its default cover from [xlenore/ps2-covers](https://github.com/xlenore/ps2-covers) and writes it beneath the `covers` directory at the root of `PS3MGR_PS2_GAME_DIR`:

```text
/data/ps2/
├── Gran Turismo 4.iso
├── God of War.iso
└── covers/
    ├── SCES-51719.jpg
    ├── default/
    │   └── SCUS-97399.jpg
    └── 3d/
        └── SCUS-97399.png
```

The downloaded cache uses `covers/<SERIAL>.jpg`. Existing files always take priority and cause no network request. The `default/<SERIAL>.jpg` and `3d/<SERIAL>.png` layouts remain compatible with manually copied repository files. JPG, JPEG, PNG, and WebP are supported, with case-insensitive extensions and serial separators. Existing same-name images beside an ISO have highest priority.

Only current games with known serials are requested. Downloads are bounded, validated as JPEG/PNG, and committed atomically; failures leave the game visible with its built-in placeholder. Set `PS3MGR_PS2_COVER_DOWNLOAD=false` to prohibit new downloads and use cached/manual images only. Once cached, covers work without internet access. The browser never receives a remote image URL: the embedded UI serves CSS, JavaScript, covers, and API calls from the application origin under a same-origin content security policy.

## Commands

```text
ps3mgr --help
ps3mgr ps2 --help
ps3mgr ps2 local-games [--dir /data/ps2] [--size] [--json]
ps3mgr ps2 games [--dir /data/ps2] [--json]
ps3mgr ps2 usb [--json]
ps3mgr ps2 compare --usb usb0 [--json]
ps3mgr ps2 install --usb usb0 "God of War" "Gran Turismo 4"
ps3mgr ps2 install --usb usb0 --all
ps3mgr ps2 queue [--json]
ps3mgr ps5 --help
ps3mgr ps5 local-games [--dir /data/ps5] [--json]
ps3mgr ps5 scan 192.168.1.0/24 [--workers 32] [--json]
ps3mgr ps5 add-console --ip 192.168.1.155 [--json]
ps3mgr ps5 games --ip 192.168.1.155 [--json]
ps3mgr ps5 compare --ip 192.168.1.155 [--dir /data/ps5] [--json]
ps3mgr ps5 install --ip 192.168.1.155 "PPSA01325"
ps3mgr ps5 install --ip 192.168.1.155 --all
ps3mgr ps5 queue [--json]
ps3mgr local-games [--dir /data/games] [--json]
ps3mgr scan 192.168.1.0/24 [--workers 32] [--json]
ps3mgr consoles [--json]
ps3mgr add-console --ip 192.168.1.152 [--json]
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
| `PS3MGR_PS3_GAME_DIR` | value of `PS3MGR_GAME_DIR` | Preferred generation-specific PS3 game directory override |
| `PS3MGR_PS2_GAME_DIR` | `./ps2-games` | Recursive local PS2 ISO library |
| `PS3MGR_PS2_SYSTEM_DIR` | `./ps2-system` | Required OPL/system files copied to a selected USB target |
| `PS3MGR_PS2_USB_MOUNT_ROOT` | `/mnt/usb` | Parent containing discovered mounted USB directories |
| `PS3MGR_PS2_COVER_DOWNLOAD` | `true` | Download only missing known-serial covers and cache them locally |
| `PS3MGR_PS5_GAME_DIR` | `./ps5-games` | Recursive local PS5 ShadowMountPlus library |
| `PS3MGR_PS5_REMOTE_GAME_DIR` | `/data/etaHEN/games` | PS5 FTP installation/library directory |
| `PS3MGR_PS5_FTP_PORT` | `2121` | etaHEN FTP port used for PS5 scan and transfers |
| `PS3MGR_PS5_FTP_USER` | `anonymous` | PS5 FTP username |
| `PS3MGR_PS5_FTP_PASSWORD` | empty | PS5 FTP password |
| `PS3MGR_REMOTE_GAME_DIR` | `/dev_hdd0/GAMES` | PS3 destination/library directory |
| `PS3MGR_LISTEN` | `127.0.0.1:8080` | Web listen address |
| `PS3_FTP_USER` | `anonymous` | FTP username |
| `PS3_FTP_PASSWORD` | empty | FTP password |
| `PS3MGR_WORKERS` | based on CPU, maximum 32 | Concurrent network scan workers |
| `PS3MGR_SCAN_TIMEOUT` | `500ms` | Fast TCP probe timeout for each address |
| `PS3MGR_FTP_TIMEOUT` | `8s` | FTP operation and connection timeout |

Durations use Go syntax such as `500ms`, `8s`, or `2m`.

`serve` writes structured text logs suitable for Docker collection. Startup logs show configured library/USB paths, separate PS2, PS3, and PS5 game counts, discovered USB capacity, FTP ports, and stable section URLs. Runtime logs cover console discovery and each platform's queue lifecycle with `[PS2]`, `[PS3]`, or `[PS5]` messages. FTP credentials are never logged, and high-frequency progress stays on the event stream instead of flooding stdout.

## PS2 / Open PS2 Loader behavior

The PS2 scanner accepts `.iso` case-insensitively and reads `SYSTEM.CNF` directly from ISO9660 images to identify serials including `SLUS`, `SLES`, `SCUS`, `SCES`, `SLPM`, `SLPS`, and related legitimate prefixes. Filename detection is a fallback; an unidentified ISO remains visible as `unknown` but cannot be installed until it has a reliable game ID.

For FAT32-compatible images, the installer writes OPL's `DVD/<SERIAL>.<title>.iso` layout. Images above the FAT32 single-file limit use the official USBExtreme layout: 1 GiB `ul.<crc>.<serial>.<part>` files plus fixed 64-byte records in the root `ul.cfg`. Existing unrelated `ul.cfg` records are preserved. Writes use `.partial` files, support cancellation, and are verified before completion.

The configured PS2 system directory must exist and contain files before installation. The installer prepares standard OPL directories (`DVD`, `CD`, `ART`, `CFG`, `VMC`, `THM`, and `APPS`) and copies the configured system tree without invoking Bash, `cp`, `df`, `iso2opl`, or another shell utility.

## PS5 / ShadowMountPlus behavior

The PS5 module follows the [ShadowMountPlus layout](https://github.com/drakmor/ShadowMountPlus): games are discovered below a dedicated local root and uploaded to `/data/etaHEN/games` by default. It recognizes direct game folders containing `sce_sys/param.json` and every documented image source: `.ffpkg`, `.exfat`, `.ffpfs`, and `.ffpfsc`. Folder metadata supplies the title ID, localized title, and local icon where available; PPSA and CUSA IDs are also detected in filenames. As upstream requires, `backports/` is excluded from normal game discovery; `.ffpfs` and `.ffpfsc` remain marked as upstream experimental formats but can be copied normally.

Network discovery probes port 2121 and verifies both `/data` and `/data/etaHEN` markers before registering a host as a PS5. The web API accepts a console ID and local game IDs, never an arbitrary remote filesystem path. PS5 transfers are sequential within their own queue, but run concurrently with PS2 and PS3 jobs.

## PS3 and network requirements

- Enable an FTP server on the PS3 (commonly through webMAN MOD, multiMAN, or another trusted homebrew environment).
- Ensure the computer and PS3 can reach one another on the local network and TCP port 21 is permitted.
- Use only a private/local IPv4 CIDR that you are authorized to administer. Public networks and ranges larger than `/16` are rejected.
- The detector confirms PS3 filesystem markers such as `/dev_hdd0` or `/dev_flash`; an arbitrary FTP server is not reported as a PS3.
- Configure credentials if the FTP server does not allow anonymous access.

The FTP client supports passive mode (EPSV with PASV fallback), binary transfers, directory creation, per-file retries, reconnection, `REST`-based resume where supported, and cancellation. A completed remote file is skipped during a resumed installation.

## PS3 transfer behavior

PS3 callers use the PS3 FTP queue and games transfer one at a time. PS2 uses a separate OPL/USB queue that can run concurrently. A failed or cancelled PS3 game does not stop later PS3 games by default; use `--stop-on-error` in the CLI or the corresponding API field to cancel the remainder of that submitted PS3 queue.

The web panel provides:

- searchable, sortable, filterable game cards and cover images;
- select all, select missing, and selection size totals;
- private CIDR discovery and console selection;
- direct PS3 IP addition when the address is already known;
- per-console game refresh that updates installed/missing status;
- installed/missing comparison using title ID first and normalized title second;
- live SSE progress, speed, ETA, current file, retry, cancellation, pause-after-current, and clear-completed controls;
- in-page completion/error toasts and optional browser notifications.

`Pause` prevents the next game from starting; it does not interrupt the active FTP upload. `Cancel` uses context cancellation for the active game. The queue then continues to the next game unless stop-on-error was requested.

## HTTP API

The embedded web UI uses the same public application services as the CLI:

```text
GET    /api/health
GET    /api/ps2/games
GET    /api/ps2/games/{id}/cover
GET    /api/ps2/usb
GET    /api/ps2/usb/status
POST   /api/ps2/usb/{id}/prepare
GET    /api/ps2/compare/{usb_id}
POST   /api/ps2/queue
GET    /api/ps2/queue
GET    /api/ps2/queue/{id}
POST   /api/ps2/queue/{id}/cancel
POST   /api/ps2/queue/{id}/retry
GET    /api/ps5/games
GET    /api/ps5/games/{id}/icon
POST   /api/ps5/scan
GET    /api/ps5/consoles
POST   /api/ps5/consoles
GET    /api/ps5/consoles/{id}/games
POST   /api/ps5/consoles/{id}/rescan
GET    /api/ps5/compare/{id}
POST   /api/ps5/queue
GET    /api/ps5/queue
GET    /api/ps5/queue/{id}
POST   /api/ps5/queue/{id}/cancel
POST   /api/ps5/queue/{id}/retry
POST   /api/ps5/queue/pause
POST   /api/ps5/queue/resume
DELETE /api/ps5/queue/completed
GET    /api/local-games
GET    /api/local-games/{id}/icon
POST   /api/scan
GET    /api/consoles
POST   /api/consoles
GET    /api/consoles/{id}
GET    /api/consoles/{id}/games
POST   /api/consoles/{id}/rescan
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

Connect a known PS3 directly:

```sh
curl -X POST http://127.0.0.1:8080/api/consoles \
  -H 'Content-Type: application/json' \
  -d '{"ip":"192.168.1.152"}'
```

Refresh its installed games and comparison:

```sh
curl -X POST http://127.0.0.1:8080/api/consoles/192.168.1.152/rescan
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

The default TCP probe timeout is `500ms`, independent of the longer FTP timeout. On unusually slow Wi-Fi, increase it with `PS3MGR_SCAN_TIMEOUT=1s`. If the address is already known, use **PS3 Consoles → Add PS3 by IP** instead of scanning.

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

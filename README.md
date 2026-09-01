# C-Gate Server Container

> **v1.1.4**

Containerised [SpaceLogic C-Gate Server](https://www.se.com/au/en/product-range/63702-spacelogic-c-gate/) with a built-in web console for testing and debugging C-Bus networks. The bundled C-Gate build is selectable at image build time — see [C-Gate Version](#c-gate-version).

## Quick Start

```bash
docker compose up -d
```

C-Gate will start with the default project `HOME` and the web console will be available at [http://localhost:8980](http://localhost:8980).

## Ports

| Port | Protocol | Description |
|------|----------|-------------|
| 20023 | TCP | Command Interface |
| 20024 | TCP | Event Interface |
| 20025 | TCP | Status Change Port (SCP) |
| 20026 | TCP | Config Change Port (CCP) |
| 20123–20126 | TCP | SSL equivalents of the above |
| **8980** | **HTTP** | **Web Console / HTTP Commander** |

## Web Console

The container includes a lightweight web-based console for interacting with C-Gate — useful for testing, debugging, and visualising C-Bus traffic without needing a dedicated client application.

Open [http://localhost:8980](http://localhost:8980) in a browser to get a terminal-style interface that provides:

- **Command entry** — type any C-Gate command and see the response immediately
- **Live streaming** — events and status changes from the C-Bus network appear in real time via WebSocket
- **Stream filtering** — toggle visibility of events, status updates, commands, and responses
- **Command history** — use arrow keys to recall previous commands
- **Project tag databases** — back up, download and upload whole projects, see [below](#project-tag-databases)

### Project tag databases

**Back up &lt;project&gt;** in the console header sends `project save`, waits for C-Gate to
write the project out, and downloads the whole project directory — database, dynamic
labelling bitmaps and index — as a `.cbz`, which is the shape and the name C-Bus Toolkit
restores from.

The save is the point of it. C-Gate holds a loaded project in memory and writes it to disk
only when told to, so a backup taken without one is whatever was last saved. If C-Gate is
unreachable the download still happens against the copy on disk and the log says it may be
out of date.

**Tag database** opens a panel listing every project under `tag/`, with its file count, size
and when its database was last written. Each project can be downloaded as `.db` (the database
alone) or, when there is more beside it, `.zip` and `.cbz` — the same archive under two names.
Nothing in this table sends `project save` first, so use the header button when the project
has changed since it was loaded.

**Upload** takes a Toolkit `.cbz`, a `.zip` or `.tar` of a project directory, or a bare
`.db`. The file is identified by its contents rather than its name, and the project name
comes from the database inside it — so Toolkit's `YELMAH_09_May_2025_2214_1.18.1.cbz` needs
no renaming. An upload is unpacked into a staging directory first, so nothing in place is
touched until a complete project has landed; the project is then stopped and closed in
C-Gate, the old copy moved aside as `<project>.bak`, and the project loaded and started
again. Uploads are capped at 64 MB, expanding to at most 256 MB across 4096 files, and
entries that would be written outside the project directory are refused.

Which project counts as "in use" is normally asked of C-Gate (`PROJECT USE` answers
`123 project=NAME`). Set `CGATE_PROJECT` in the environment to override that — see
[Project Files](#project-files).

### HTTP Commander

You can also send commands directly via HTTP GET requests — handy for scripting, `curl`, or integrating with other tools:

```
http://localhost:8980/cgate?cmd=ON%20//HOME/254/56/120
```

Returns a JSON response:

```json
{
  "cmd": "ON //HOME/254/56/120",
  "response": ["200 OK: //HOME/254/56/120"]
}
```

More examples:

```bash
# Get the C-Gate version
curl "http://localhost:8980/cgate?cmd=version"

# List projects
curl "http://localhost:8980/cgate?cmd=project%20list"

# Turn off a group
curl "http://localhost:8980/cgate?cmd=OFF%20//HOME/254/56/120"

# Ramp a group to 50% over 4 seconds
curl "http://localhost:8980/cgate?cmd=RAMP%20//HOME/254/56/120%2050%25%204s"

# Get all group levels on an application
curl "http://localhost:8980/cgate?cmd=GET%20//HOME/254/56/*%20level"
```

### Health and readiness

Two probes, for two different questions:

| Endpoint | Answers | Use it to |
|----------|---------|-----------|
| `/health` | Is the bridge serving? Always `200` while it is. | Decide whether to restart the container. |
| `/ready` | Are all C-Gate connections up? `503` until they are. | Gate a client's first poll. |

Both return the same body:

```json
{
  "status": "ok",
  "connections": { "command": true, "event": true, "status": true }
}
```

`/health` stays `200` even when C-Gate is unreachable — it is a liveness probe,
and C-Gate can take up to a minute to sync its networks on a cold start, so
failing it during that window would turn a normal boot into a restart loop.
Read `status` in the body for the real picture.

Wait for `/ready` before polling group levels. Commands issued while C-Gate is
still syncing return `408 Operation failed`; this is expected and self-clearing,
so gate on readiness rather than building a retry loop around it. Note that
`/ready` reflects the bridge's own connections, not C-Gate's network state — a
project can still be mid-sync when it first passes, so treat a `408` after that
point as transient too.

A command sent while C-Gate is down returns `502` immediately rather than
blocking until it comes back, and the bridge reconnects on its own within a few
seconds of C-Gate returning — no request is needed to prompt it.

## Configuration

### Project Files

Project tag databases are stored in the `tag/` directory and bind-mounted into the container. The default project `HOME` is included.

> **Important:** Each C-Gate project must reside in a subfolder of `tag/` whose name matches the project name. For example, the `HOME` project database must be at `tag/HOME/HOME.db`.

```
tag/
├── HOME/
│   └── HOME.db
└── EXAMPLE/
    └── EXAMPLE.db
```

The web console reads and writes this directory: it lists what is there, serves each project
for download, and installs uploads into the same layout. Nothing else is written to it, and
a database placed anywhere but `tag/<project>/<project>.db` is invisible to C-Gate.

The console marks one project **(in use)**, and the header's backup button targets it. That
name is asked of C-Gate on first use and cached. If C-Gate gives the wrong answer — or you
want the console pinned to one project regardless — set it explicitly:

```yaml
services:
  cgate:
    environment:
      # Optional. Left unset, the console asks C-Gate which project is loaded.
      CGATE_PROJECT: HOME
      # Optional. Where the console looks for projects; must match the bind mount.
      CGATE_PROJECTS_DIR: /cgate/tag
```

### C-Gate Version

The C-Gate distribution bundled into the image is selected by the `CGATE_VERSION`
build argument, which defaults to `3.8.0_2348`.

To build a different version, unzip the Schneider distribution into
`C-Gate Downloads/` and build with its version — no `Dockerfile` edit needed:

```bash
# docker compose (or set CGATE_VERSION in a .env file)
CGATE_VERSION=3.7.1 docker compose build

# plain docker
docker build --build-arg CGATE_VERSION=3.7.1 -t cgate-server .
```

Matching is deliberately lenient, so the vendor zip can be dropped in as-is.
Schneider has changed the packaging between releases, so the layout inside a
version folder is discovered rather than assumed:

```
C-Gate Downloads/
├── cgate-3.7.0_2285/
│   └── cgate/          ← 3.7.0 nests the tree under cgate/
│       └── cgate.jar
├── cgate-3.8.0_2348/
│   └── cgate.jar       ← 3.8.0 ships it flattened
└── cgate-3.9.0_2400/
    └── CGate/          ← any other folder name also works
        └── cgate.jar
```

`CGATE_VERSION` accepts the exact directory name (`cgate-3.8.0_2348` or
`3.8.0_2348`) or an unambiguous prefix (`3.8.0`). Within the matched directory
the tree root is whichever location holds `cgate.jar` — the folder itself, or a
single sub-folder of any name. Two sub-folders each holding a `cgate.jar` is an
error rather than a guess.

The build fails with an actionable message if the version is missing, matches
more than one directory, or contains no `cgate.jar`:

```
ERROR: CGATE_VERSION='3.7' is ambiguous. Matches:
  cgate-3.7.0_2285
  cgate-3.7.1_2300
Set CGATE_VERSION to one of these exact directory names.
```

The build log prints which distribution it resolved to, and its version and
build number:

```
Using C-Gate distribution: cgate-3.7.1_2300/cgate
  Version: 3.7.1
  Build:   2300
```

The value you passed is recorded on the image as the `cgate.version` label, and
images published by CI carry a matching `cgate-<version>` tag alongside `latest`:

```bash
docker inspect --format '{{index .Config.Labels "cgate.version"}}' cgate-server:latest
```

Because that label echoes the argument rather than the resolved directory, the
authoritative build number lives in `/cgate/BuildInfo.txt` inside the image:

```bash
docker compose exec cgate cat /cgate/BuildInfo.txt
```

### Access Control

Edit `config/access.txt` to control which hosts can connect to C-Gate and at what privilege level:

```
interface 127.0.0.1 Program
interface 0.0.0.0 Program
```

The default configuration allows programming access from any IP address, which is appropriate for a containerised deployment behind a firewall. Restrict this in production environments.

### Overriding Defaults

The default C-Gate startup flags (`-connect localhost -project HOME`) can be overridden at runtime:

```bash
docker compose run --rm cgate -connect 192.168.1.50 -project MYBUILDING
```

## Architecture

```
                    ┌──────────────────────────────────┐
                    │          Docker Container         │
                    │                                   │
Browser ──HTTP/WS──►│  Go web bridge (:8980)            │
                    │       │                           │
                    │       ├──TCP──► Command  (:20023) │
                    │       ├──TCP──► Event    (:20024) │
Telnet/Client ─────►│       └──TCP──► Status   (:20025) │
                    │                                   │
                    │         C-Gate Server (Java)      │
                    │              ▲                     │
                    │              │                     │
                    │       tag/ config/ (volumes)      │
                    └──────────────────────────────────┘
```

The web bridge is a single static Go binary (~5 MB) that runs alongside C-Gate inside the container. It connects to C-Gate's TCP command, event, and status ports on localhost and exposes them over HTTP and WebSocket.

## Building

### Local Build

```bash
docker compose build
```

### Multi-Architecture

The included GitHub Actions workflow builds and pushes multi-arch images (`linux/amd64` and `linux/arm64`) to GitHub Container Registry on every push to `main`:

```bash
docker pull ghcr.io/<owner>/c-gate-server-container:latest
```

## Volumes

| Mount | Container Path | Description |
|-------|---------------|-------------|
| `./config` | `/cgate/config` | Access control and C-groups configuration |
| `./tag` | `/cgate/tag` | Project tag databases |

## Logging

C-Gate uses a custom [Logback](https://logback.qos.ch/) configuration (`config/logback.xml`) that provides dual-output logging:

- **Console (stdout)** — C-Gate logs are written directly to stdout via Logback's `ConsoleAppender`, making them available natively through `docker compose logs` and container management tools like Portainer. No log-tailing workarounds are needed.
- **Rolling file** — Logs are also written to `logs/event.txt` inside the container (mounted at `./C-Gate-Native-Logs` on the host), with daily rotation and a 500 KB size trigger. Up to 10 days of history are retained.

Both appenders run at `DEBUG` level by default. Edit `config/logback.xml` to adjust levels or patterns.

The whole `config/` directory is baked into the image, so `docker run` works without
any bind mount. The `./config` mount in `docker-compose.yml` overrides it, which is
what makes edits take effect on a restart rather than a rebuild.

Docker's JSON file log driver adds a second layer of rotation for the stdout stream (10 MB max, 5 rotated files), so container logs stay bounded even if Logback output is verbose.

```bash
# Follow live container logs
docker compose logs -f cgate

# Native C-Gate log files on the host
ls ./C-Gate-Native-Logs/
```

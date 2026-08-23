# C-Gate Server Container

## Project Overview

Containerised Schneider Electric SpaceLogic C-Gate Server with a Go-based web console bridge. The bundled C-Gate build is selected by the `CGATE_VERSION` build arg (default `3.7.0_2285`) — see **C-Gate Version Selection** below. The container packages a proprietary Java application (C-Gate) alongside a custom Go HTTP/WebSocket proxy for browser-based debugging of C-Bus home automation networks.

**Current version:** v1.0.1 (see `VERSION` file)

## Architecture

```
Docker Container
├── C-Gate Server (Java 11, cgate.jar)  — the core C-Bus protocol server
│   ├── Command port :20023
│   ├── Event port   :20024
│   ├── Status port  :20025
│   └── Config port  :20026
└── Go web bridge (cgate-web binary)    — HTTP/WS proxy on :8980
    ├── /        → embedded console.html (single-page terminal UI)
    ├── /cgate   → HTTP command API (GET with ?cmd= param)
    ├── /ws      → WebSocket stream (events, status, commands, responses)
    └── /health  → health check endpoint
```

- `entrypoint.sh` starts the Go bridge in a restart loop, then `exec`s the Java process as PID 1.
- Logback config (`config/logback.xml`) is injected via `-Dlogback.configurationFile` JVM flag.
- The Dockerfile copies the whole `config/` directory, not named files, so the image is
  self-contained without a bind mount. Copying files individually had omitted
  `logback.xml`, leaving that JVM flag pointing at a nonexistent path under plain
  `docker run`. Add config files freely — they ship automatically.
- The Go binary is built in a multi-stage Docker build (`golang:1.25-alpine` → `eclipse-temurin:11-jre`).
- A `cgate-dist` build stage resolves `CGATE_VERSION` to a distribution tree and stages it at `/dist`, so the runtime stage can `COPY --from` a fixed path.

## Key Files

| File | Purpose |
|------|---------|
| `Dockerfile` | Multi-stage build: Go web bridge + Java C-Gate runtime |
| `docker-compose.yml` | Service definition with ports, volumes, logging config |
| `entrypoint.sh` | Container entrypoint — starts web bridge, execs C-Gate |
| `web/main.go` | Go web bridge source (HTTP API, WebSocket hub, TCP client) |
| `web/console.html` | Embedded single-page web console UI |
| `config/logback.xml` | Logback config — ConsoleAppender (stdout) + RollingFileAppender |
| `config/access.txt` | C-Gate access control (IP-based privilege levels) |
| `config/C-groups.txt` | C-Bus group definitions |
| `tag/` | Project databases — each project in a subfolder matching its name |
| `VERSION` | Project version (semver) |
| `.github/workflows/docker-build.yml` | CI: multi-arch build (amd64/arm64) → ghcr.io |
| `C-Gate Downloads/` | Upstream C-Gate distributions, one `cgate-<version>/` dir each |

## C-Gate Version Selection

The C-Gate distribution is chosen at image build time via the `CGATE_VERSION`
build arg (default `3.7.0_2285`), resolved against directories in `C-Gate Downloads/`.

Resolution is lenient by design — the goal is "unzip a new distribution into
`C-Gate Downloads/`, build with its version, change nothing else":

1. Exact directory name — `cgate-3.7.1_2300` or `3.7.1_2300`.
2. Otherwise an unambiguous prefix — `3.7.1` matches `cgate-3.7.1_2300`.
   Multiple matches are an error listing them, not a silent pick.
3. Inside the match, the tree root is found by locating `cgate.jar` rather than
   assuming a folder name — either `<dir>/cgate.jar` (3.8.0_2348 ships this way)
   or `<dir>/<anything>/cgate.jar` (3.7.0_2285 nests it under `cgate/`). Two
   sub-folders each holding a `cgate.jar` is an error, not a guess.

Missing version, ambiguous prefix, and no-`cgate.jar` each fail the build with a
message naming what was found. The resolved path and `BuildInfo.txt` version and
build number are echoed into the build log.

Wiring:
- `Dockerfile` — global `ARG CGATE_VERSION`; the `cgate-dist` stage resolves and
  validates it, staging the tree at `/dist`; the runtime stage does
  `COPY --from=cgate-dist` and records the value as the `cgate.version` label.
- `docker-compose.yml` — passes it through `build.args`, overridable with a
  `CGATE_VERSION` env var or `.env` file.
- `.github/workflows/docker-build.yml` — sets it as a workflow-level env var,
  passes it as a build arg, and publishes a matching `cgate-<version>` image tag.

The version is deliberately **not** repeated in prose — the build arg default in
`Dockerfile` is the source of truth.

Two caveats worth knowing:
- The `cgate.version` label echoes the *argument*, not the resolved directory, so
  a prefix build labels the image `3.7.1` while the tree may be `3.7.1_2300`.
  `/cgate/BuildInfo.txt` in the image is authoritative.
- The staging stage exists because the source path contains a space, which rules
  out the shell form of `COPY` that expands build args; resolving in a `RUN`
  keeps `CGATE_VERSION` out of any `COPY` path.

## Tag Directory Convention

Each C-Gate project database must live in `tag/<PROJECT_NAME>/<PROJECT_NAME>.db`. For example:
```
tag/HOME/HOME.db
tag/EXAMPLE/EXAMPLE.db
```
This structure is bind-mounted into the container at `/cgate/tag`.

## Logging

Dual-output via Logback:
- **stdout** (ConsoleAppender) → captured by Docker json-file driver (10MB max, 5 rotated files)
- **Rolling file** → `logs/event.txt` inside container, mounted at `./C-Gate-Native-Logs/` on host

Both at DEBUG level. Pattern: `%msg%n` (raw C-Gate output, no timestamps — C-Gate adds its own).

## Development Notes

- **No test suite** — this is a containerised wrapper around a proprietary Java application. Validation is done by building the image and running the container.
- **Go web bridge** has no external dependencies beyond `golang.org/x/net/websocket`. No go.mod is committed — it's generated during Docker build (`go mod init`).
- **C-Gate jar and libs are binary blobs** checked into `C-Gate Downloads/`. Do not modify these.
- The `com/` directory (gitignored) contains a compiled Java class (`InvalidLogbackConfigException.class`) that is a C-Gate runtime artifact — leave it alone.
- Line endings matter — `entrypoint.sh` must be LF, not CRLF (past bug: `043c9eb`).

## Building & Running

```bash
# Build and start
docker compose build
docker compose up -d

# View logs
docker compose logs -f cgate

# Web console
open http://localhost:8980

# Send a command via HTTP
curl "http://localhost:8980/cgate?cmd=version"
```

## Versioning

- Semantic versioning (major.minor.patch) tracked in `VERSION` file and README header.
- Git tags: `v1.0.0`, etc.
- Docker image tags via GitHub Actions: `latest` (main branch), `sha-<commit>`, `pr-<number>`.

## Common Tasks

- **Add a new C-Gate project**: Create `tag/<NAME>/<NAME>.db` and restart the container.
- **Change log level**: Edit `config/logback.xml`, change `<root level="DEBUG">` to INFO/WARN/etc. Restart container.
- **Modify web console UI**: Edit `web/console.html` (embedded via `//go:embed`). Rebuild image.
- **Update access control**: Edit `config/access.txt`. Restart container.
- **Override startup flags**: `docker compose run --rm cgate -connect <ip> -project <name>`
- **Change C-Gate version**: Add `C-Gate Downloads/cgate-<version>/cgate/`, then build with `CGATE_VERSION=<version> docker compose build`.

# C-Gate Server Container

## Project Overview

Containerised Schneider Electric SpaceLogic C-Gate Server (v3.7.0, build 2285) with a Go-based web console bridge. The container packages a proprietary Java application (C-Gate) alongside a custom Go HTTP/WebSocket proxy for browser-based debugging of C-Bus home automation networks.

**Current version:** v1.0.0 (see `VERSION` file)

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
- The Go binary is built in a multi-stage Docker build (`golang:1.25-alpine` → `eclipse-temurin:11-jre`).

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
| `C-Gate Downloads/` | Upstream C-Gate distribution (v3.7.0 build 2285) |

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

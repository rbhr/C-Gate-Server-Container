# C-Gate Server Container

Containerised [SpaceLogic C-Gate Server](https://www.se.com/au/en/product-range/63702-spacelogic-c-gate/) (v3.7.0 build 2285) with a built-in web console for testing and debugging C-Bus networks.

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

## Configuration

### Project Files

Project tag databases are stored in the `tag/` directory and bind-mounted into the container. The default project `HOME` is included — add your own `.db` files here.

```
tag/
├── HOME.db
└── EXAMPLE.db
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

Container logs are managed by Docker's JSON file driver with rotation (10 MB max, 5 files). View logs with:

```bash
docker compose logs -f cgate
```

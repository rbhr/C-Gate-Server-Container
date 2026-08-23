## C-Gate distribution version bundled into the image.
##
## Selects which tree under "C-Gate Downloads/cgate-<version>/cgate/" is
## installed. Declared before the first FROM so it is global; each stage that
## uses it re-declares ARG CGATE_VERSION to pull it into scope.
ARG CGATE_VERSION=3.7.0_2285

## Stage 1: Build the C-Gate web console bridge
FROM golang:1.25-alpine AS web-build
WORKDIR /build
COPY web/main.go web/console.html ./
RUN go mod init cgate-web && \
    go get golang.org/x/net/websocket && \
    CGO_ENABLED=0 go build -o cgate-web .

## Stage 2: Select the requested C-Gate distribution
##
## Staged separately rather than copied straight into the runtime image: the
## source path contains a space, so it cannot be written in the shell form of
## COPY that expands build args. Resolving the version here keeps CGATE_VERSION
## out of any COPY path, and lets a bad value fail the build with a clear
## message instead of producing a broken image.
##
## CGATE_VERSION is matched leniently so a freshly unzipped distribution can be
## dropped into "C-Gate Downloads/" and built with no other changes. It accepts:
##   - the exact directory name       cgate-3.7.1_2300  /  3.7.1_2300
##   - an unambiguous prefix          3.7.1  ->  cgate-3.7.1_2300
## and either layout inside that directory:
##   - <dir>/cgate/cgate.jar          (how the vendor zip extracts)
##   - <dir>/cgate.jar                (contents moved up a level)
FROM eclipse-temurin:11-jre AS cgate-dist
ARG CGATE_VERSION
COPY ["C-Gate Downloads/", "/downloads/"]
RUN set -eu; \
    matched=""; \
    set --; \
    for d in /downloads/*/; do \
        [ -d "$d" ] || continue; \
        d="${d%/}"; b="${d##*/}"; \
        if [ "$b" = "${CGATE_VERSION}" ] || [ "$b" = "cgate-${CGATE_VERSION}" ]; then \
            set -- "$d"; matched=exact; break; \
        fi; \
    done; \
    if [ -z "$matched" ]; then \
        for d in /downloads/*/; do \
            [ -d "$d" ] || continue; \
            d="${d%/}"; b="${d##*/}"; \
            case "$b" in \
                "${CGATE_VERSION}"*|"cgate-${CGATE_VERSION}"*) set -- "$@" "$d" ;; \
            esac; \
        done; \
    fi; \
    if [ "$#" -eq 0 ]; then \
        echo "ERROR: no C-Gate distribution matches CGATE_VERSION='${CGATE_VERSION}'" >&2; \
        echo "Looked under 'C-Gate Downloads/' for a directory named or starting with" >&2; \
        echo "'${CGATE_VERSION}' or 'cgate-${CGATE_VERSION}'. Present:" >&2; \
        ls -1 /downloads 2>/dev/null | sed 's/^/  /' >&2; \
        exit 1; \
    fi; \
    if [ "$#" -gt 1 ]; then \
        echo "ERROR: CGATE_VERSION='${CGATE_VERSION}' is ambiguous. Matches:" >&2; \
        for d in "$@"; do echo "  ${d##*/}" >&2; done; \
        echo "Set CGATE_VERSION to one of these exact directory names." >&2; \
        exit 1; \
    fi; \
    dir="$1"; \
    if [ -f "$dir/cgate/cgate.jar" ]; then src="$dir/cgate"; \
    elif [ -f "$dir/cgate.jar" ]; then src="$dir"; \
    else \
        echo "ERROR: no cgate.jar found under '${dir##*/}'" >&2; \
        echo "Expected '${dir##*/}/cgate/cgate.jar' (vendor zip layout)" >&2; \
        echo "      or '${dir##*/}/cgate.jar'." >&2; \
        exit 1; \
    fi; \
    echo "Using C-Gate distribution: ${src#/downloads/}"; \
    if [ -f "$src/BuildInfo.txt" ]; then \
        sed -n 's/^\(Version\|Build\):[[:space:]]*/  &/p' "$src/BuildInfo.txt"; \
    fi; \
    mkdir -p /dist; \
    cp -a "$src/." /dist/

## Stage 3: C-Gate server image
FROM eclipse-temurin:11-jre AS base
ARG CGATE_VERSION

LABEL maintainer="rbhr"
LABEL description="SpaceLogic C-Gate Server"
LABEL cgate.version="${CGATE_VERSION}"

# C-Gate ports
# 20023 = Command Interface
# 20024 = Event Interface
# 20025 = Status Change Port (SCP)
# 20026 = Config Change Port (CCP)
# 20123-20126 = SSL equivalents
# 8980 = Web Console / HTTP Commander
EXPOSE 20023 20024 20025 20026 20123 20124 20125 20126 8980

RUN mkdir -p /cgate/tag /cgate/config /cgate/logs

COPY --from=cgate-dist /dist/ /cgate/

# Default config and tag files are copied in but expected to be
# overridden by bind mounts at runtime.
#
# The whole config/ directory is copied rather than named files: entrypoint.sh
# passes -Dlogback.configurationFile=/cgate/config/logback.xml, and copying
# files individually had silently omitted logback.xml, so a plain `docker run`
# without a ./config bind mount started with no logback config at all. A
# directory copy also means a future config file cannot be missed the same way.
COPY config/ /cgate/config/
COPY tag/ /cgate/tag/

# Copy web console bridge binary
COPY --from=web-build /build/cgate-web /cgate/cgate-web

# Copy entrypoint wrapper
COPY entrypoint.sh /cgate/entrypoint.sh
RUN chmod +x /cgate/entrypoint.sh

WORKDIR /cgate

# Launch via entrypoint wrapper (starts web bridge, then execs C-Gate)
ENTRYPOINT ["sh", "/cgate/entrypoint.sh"]

# Run in server mode (ignores shutdown attempts, keeps container alive)
CMD ["-s"]

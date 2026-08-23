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
## out of any COPY path and lets an unknown version fail the build with a clear
## message instead of producing a broken image.
FROM eclipse-temurin:11-jre AS cgate-dist
ARG CGATE_VERSION
COPY ["C-Gate Downloads/", "/downloads/"]
RUN set -eu; \
    src="/downloads/cgate-${CGATE_VERSION}/cgate"; \
    if [ ! -d "$src" ]; then \
        echo "ERROR: no C-Gate distribution for CGATE_VERSION='${CGATE_VERSION}'" >&2; \
        echo "Expected directory: C-Gate Downloads/cgate-${CGATE_VERSION}/cgate/" >&2; \
        echo "Available versions:" >&2; \
        ls -1 /downloads | sed -n 's/^cgate-/  /p' >&2; \
        exit 1; \
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
# overridden by bind mounts at runtime
COPY config/access.txt /cgate/config/access.txt
COPY config/C-groups.txt /cgate/config/C-groups.txt
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

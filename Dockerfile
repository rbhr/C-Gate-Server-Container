## Stage 1: Build the C-Gate web console bridge
FROM golang:1.22-alpine AS web-build
WORKDIR /build
COPY web/main.go web/console.html ./
RUN go mod init cgate-web && \
    go get golang.org/x/net/websocket && \
    CGO_ENABLED=0 go build -o cgate-web .

## Stage 2: C-Gate server image
FROM eclipse-temurin:11-jre AS base

LABEL maintainer="rbhr"
LABEL description="SpaceLogic C-Gate Server"

# C-Gate ports
# 20023 = Command Interface
# 20024 = Event Interface
# 20025 = Status Change Port (SCP)
# 20026 = Config Change Port (CCP)
# 20123-20126 = SSL equivalents
# 8980 = Web Console / HTTP Commander
EXPOSE 20023 20024 20025 20026 20123 20124 20125 20126 8980

RUN mkdir -p /cgate/tag /cgate/config /cgate/logs

COPY ["C-Gate Downloads/cgate-3.7.0_2285/cgate/", "/cgate/"]

# Default config and tag files are copied in but expected to be
# overridden by bind mounts at runtime
COPY config/access.txt /cgate/config/access.txt
COPY config/C-groups.txt /cgate/config/C-groups.txt
COPY tag/ /cgate/tag/

# Copy web console bridge binary
COPY --from=web-build /build/cgate-web /cgate/cgate-web

# Copy entrypoint wrapper
COPY entrypoint.sh /cgate/entrypoint.sh

WORKDIR /cgate

# Launch via entrypoint wrapper (starts web bridge, then execs C-Gate)
ENTRYPOINT ["/cgate/entrypoint.sh"]

# Default C-Gate address and project
CMD ["-connect", "localhost", "-project", "HOME"]

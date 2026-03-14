FROM eclipse-temurin:11-jre AS base

LABEL maintainer="rbhr"
LABEL description="SpaceLogic C-Gate Server"

# C-Gate ports
# 20023 = Command Interface
# 20024 = Event Interface
# 20025 = Status Change Port (SCP)
# 20026 = Config Change Port (CCP)
# 20123-20126 = SSL equivalents
EXPOSE 20023 20024 20025 20026 20123 20124 20125 20126

RUN mkdir -p /cgate/tag /cgate/config /cgate/logs

COPY "C-Gate Downloads/cgate-3.7.0_2285/cgate/" /cgate/

# Default config and tag files are copied in but expected to be
# overridden by bind mounts at runtime
COPY config/access.txt /cgate/config/access.txt
COPY config/C-groups.txt /cgate/config/C-groups.txt
COPY tag/ /cgate/tag/

WORKDIR /cgate

# Launch C-Gate with restart support and serial port discovery
# Output goes to stdout/stderr for Docker logging
ENTRYPOINT ["java", \
    "-Djava.library.path=.", \
    "-Xms64M", \
    "-Xmx256M", \
    "-jar", "cgate.jar"]

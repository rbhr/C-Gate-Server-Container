#!/bin/sh

# Restart the web console bridge automatically if it crashes
(
  while true; do
    /cgate/cgate-web
    echo "cgate-web exited ($?) — restarting in 2s" >&2
    sleep 2
  done
) &

# Tail C-Gate log files into container stdout so they appear in
# docker logs / Portainer. Waits for files to appear, then follows.
(
  # Wait for C-Gate to create its log files
  while [ ! -d /cgate/logs ] || [ -z "$(ls /cgate/logs/*.txt 2>/dev/null)" ]; do
    sleep 2
  done
  tail -n 0 -F /cgate/logs/*.txt
) &

# Launch C-Gate as PID 1 (exec replaces shell for proper signal handling)
exec java \
    -Djava.library.path=. \
    -Xms64M \
    -Xmx256M \
    -jar cgate.jar \
    "$@"

#!/bin/sh

# Restart the web console bridge automatically if it crashes
(
  while true; do
    /cgate/cgate-web
    echo "cgate-web exited ($?) — restarting in 2s" >&2
    sleep 2
  done
) &

# Launch C-Gate as PID 1 (exec replaces shell for proper signal handling)
exec java \
    -Djava.library.path=. \
    -Xms64M \
    -Xmx256M \
    -jar cgate.jar \
    "$@"

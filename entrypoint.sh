#!/bin/sh

# Restart the web console bridge automatically if it crashes
(
  while true; do
    /cgate/cgate-web
    echo "cgate-web exited ($?) — restarting in 2s" >&2
    sleep 2
  done
) &

# Tail the active C-Gate event log into container stdout so entries
# appear in docker logs / Portainer. Uses tail -F which follows by
# name, so it handles log rollover (event.txt gets recreated).
(
  while [ ! -f /cgate/logs/event.txt ]; do
    sleep 2
  done
  tail -n 0 -F /cgate/logs/event.txt
) &

# Launch C-Gate as PID 1 (exec replaces shell for proper signal handling)
exec java \
    -Djava.library.path=. \
    -Xms64M \
    -Xmx256M \
    -jar cgate.jar \
    "$@"

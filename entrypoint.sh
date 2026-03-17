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
# Custom logback.xml adds a ConsoleAppender so C-Gate logs appear
# natively in container stdout — no tail workaround needed.
exec java \
    -Djava.library.path=. \
    -Dlogback.configurationFile=/cgate/config/logback.xml \
    -Xms64M \
    -Xmx256M \
    -jar cgate.jar \
    "$@"

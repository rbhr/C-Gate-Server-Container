#!/bin/sh
# Start the web console bridge in the background
/cgate/cgate-web &

# Launch C-Gate as PID 1 (exec replaces shell for proper signal handling)
exec java \
    -Djava.library.path=. \
    -Xms64M \
    -Xmx256M \
    -jar cgate.jar \
    "$@"

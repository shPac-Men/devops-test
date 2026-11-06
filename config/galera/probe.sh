#!/bin/bash
# Consume request headers before closing the HTTP connection.
while IFS= read -r -t 2 line && [ "$line" != $'\r' ]; do :; done
if timeout 4 /opt/galera/healthy.sh >/dev/null 2>&1; then
    printf 'HTTP/1.0 200 OK\r\nContent-Length: 3\r\n\r\nOK\n'
else
    printf 'HTTP/1.0 503 Unavailable\r\nContent-Length: 5\r\n\r\nDOWN\n'
fi

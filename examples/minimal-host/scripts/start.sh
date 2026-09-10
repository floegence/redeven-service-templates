#!/bin/sh
set -eu
exec example-server --port "$REDEVEN_SERVICE_PORT"

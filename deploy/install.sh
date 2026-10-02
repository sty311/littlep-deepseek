#!/bin/sh
set -eu
BASE=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
exec python3 "$BASE/device.py" install "$@"

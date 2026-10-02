#!/bin/sh
set -eu
BASE=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
cd "$BASE"
exec python3 scripts/project.py package

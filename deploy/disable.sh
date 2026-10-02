#!/bin/sh
set -eu
BASE=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
exec "$BASE/littlep-supervisor" disable "$@"

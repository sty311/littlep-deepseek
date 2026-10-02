#!/bin/sh
# LITTLEP-R7: bridge-only startup. No login/authentication changes.
[ -x /userdisk/littlep-bridge/start.sh ] || exit 0
/userdisk/littlep-bridge/start.sh boot >/dev/null 2>&1
exit 0

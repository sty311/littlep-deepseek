# Autostart and lifecycle

The tested firmware already runs `/userdisk/skip_re/skip_login.sh` from `S99_run_test_scripts`. The caller SHA is checked before installation. The name is a stock hook location; the **project script only starts the bridge**, with no login or authentication change. Rootfs init scripts are neither distributed nor modified.

Files live in `/userdisk/littlep-bridge/`: binary, supervisor, private `config.json`, six management scripts, `run/`, `DISABLED`, `bridge.log` and `.1`. The Linux supervisor launches the bridge with this private config. Integrated endpoint, health and supervisor use 127.0.0.1:18181. Advanced test environment overrides are for local fixtures, not a LAN deployment.

PID identity includes process starttime and executable, with a lifetime flock. A stale PID cannot authorize killing an unrelated process. Repeated start remains one instance. Unexpected exit uses 3/5/10/30-second backoff; rapid crashes hold 30 seconds. Healthy long runs reset failure counts. Logs are filtered and rotated at 3 MiB with one previous file. No question, reasoning, answer, key or full request header is intended to persist in supervisor logs.

| Command | Behavior |
|---|---|
| start.sh | Start if enabled; do not duplicate |
| status.sh | Process identity, localhost health/version |
| restart.sh | Stop managed service and restart; clears memory sessions |
| stop.sh / disable.sh | Persist DISABLED and stop managed processes |
| enable.sh | Remove disabled flag and start |

Boot does not wait on Wi-Fi/DeepSeek; request errors handle network absence. No fixed 30-second startup sleep. Integration tests simulate port contention, stale/unrelated PID, repeated start, crash recovery/backoff, log rotation and disable/enable. Earlier device acceptance tested two normal reboots and idle crash recovery (~3.16 s); power-cut startup and arbitrary concurrent failure scenarios are not claimed.

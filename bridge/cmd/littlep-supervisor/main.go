//go:build linux

// LittleP r7 lifecycle supervisor. This program never reads the bridge's private configuration.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "r7-autostart"
const bridgeVersion = "0.7.0-source-ready"
const maxLog = 3 * 1024 * 1024

type config struct {
	root, run, bridge, log, disabled, supervisorPID, childPID, stateFile, lock, health, address, reason string
	backoff                                                                                             []time.Duration
	healthyReset                                                                                        time.Duration
}
type identity struct {
	PID   int    `json:"pid"`
	Start string `json:"starttime"`
	Exe   string `json:"exe"`
}
type lifecycle struct {
	Failures     int    `json:"failures"`
	RestartCount int    `json:"restart_count"`
	LastStart    string `json:"last_start_utc"`
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func settings() config {
	root := env("R7_ROOT", "/userdisk/littlep-bridge")
	run := filepath.Join(root, "run")
	port := env("R7_PORT", "18181")
	return config{root: root, run: run, bridge: env("R7_BRIDGE", filepath.Join(root, "littlep-bridge")), log: filepath.Join(root, "bridge.log"), disabled: filepath.Join(root, "DISABLED"), supervisorPID: filepath.Join(run, "r7-supervisor.json"), childPID: filepath.Join(run, "r7-bridge.json"), stateFile: filepath.Join(run, "r7-state.json"), lock: filepath.Join(run, "r7-supervisor.lock"), health: "http://127.0.0.1:" + port + "/health", address: "127.0.0.1:" + port, backoff: []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}, healthyReset: 60 * time.Second}
}
func starttime(pid int) (string, error) {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return "", errors.New("malformed proc stat")
	}
	f := strings.Fields(string(b[i+2:]))
	if len(f) < 20 {
		return "", errors.New("short proc stat")
	}
	return f[19], nil // field 22, following pid and comm
}
func currentIdentity(pid int) (identity, error) {
	st, e := starttime(pid)
	if e != nil {
		return identity{}, e
	}
	exe, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if e != nil {
		return identity{}, e
	}
	return identity{pid, st, exe}, nil
}
func expectedExe(path string) string {
	v, e := filepath.EvalSymlinks(path)
	if e != nil {
		return path
	}
	return v
}
func readIdentity(path, expected string) (identity, bool) {
	var id identity
	b, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(b, &id) != nil || id.PID <= 1 {
		return id, false
	}
	now, e := currentIdentity(id.PID)
	return id, e == nil && now.Start == id.Start && now.Exe == id.Exe && now.Exe == expectedExe(expected)
}
func writeIdentity(path string, pid int) error {
	id, e := currentIdentity(pid)
	if e != nil {
		return e
	}
	b, _ := json.Marshal(id)
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func writeState(path string, s lifecycle) {
	b, _ := json.Marshal(s)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0600) == nil {
		_ = os.Rename(tmp, path)
	}
}
func readState(path string) lifecycle {
	var s lifecycle
	b, e := os.ReadFile(path)
	if e == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}
func occupied(c config) bool {
	listener, e := net.Listen("tcp", c.address)
	if e == nil {
		listener.Close()
		return false
	}
	return true
}
func enabled(c config) bool { _, e := os.Stat(c.disabled); return os.IsNotExist(e) }
func health(c config) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.health, nil)
	client := http.Client{Timeout: 350 * time.Millisecond}
	res, e := client.Do(req)
	if e != nil {
		return false, ""
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false, ""
	}
	var v struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2048)).Decode(&v) != nil || !v.OK {
		return false, ""
	}
	return true, v.Version
}
func status(c config) map[string]interface{} {
	exe, _ := os.Executable()
	sup, supOK := readIdentity(c.supervisorPID, exe)
	child, childOK := readIdentity(c.childPID, c.bridge)
	h, v := false, ""
	if childOK {
		h, v = health(c)
		h = h && v == bridgeVersion
	}
	bootID, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	st := readState(c.stateFile)
	state := "stopped"
	if supOK && childOK {
		state = "running"
	}
	result := map[string]interface{}{"state": state, "listen_address": c.address, "failures": st.Failures, "restart_count": st.RestartCount, "last_start_utc": st.LastStart, "supervisor_version": version, "expected_bridge_version": bridgeVersion, "boot_id": strings.TrimSpace(string(bootID)), "enabled": enabled(c), "supervisor_running": supOK, "bridge_running": childOK, "bridge_healthy": h, "bridge_version": v}
	if supOK {
		result["supervisor_pid"] = sup.PID
	}
	if childOK {
		result["bridge_pid"] = child.PID
	}
	if occupied(c) && !childOK {
		result["port_occupied_by_other"] = true
	}
	return result
}
func printStatus(c config) { b, _ := json.Marshal(status(c)); fmt.Println(string(b)) }

// Only known, bounded, token-like diagnostic values are persisted. In particular,
// query, request bodies, reasons, URLs, and unrecognized child lines are discarded.
var tokenValue = regexp.MustCompile(`^[A-Za-z0-9._:/+%=,\[\]-]{1,160}$`)
var allowedEvents = map[string]bool{"listening": true, "scan_input": true, "batch": true, "completed": true, "rejected": true, "upstream_failed": true, "client_write_failed": true, "stream_interrupted": true, "stream_failed": true, "scan_multimodal_constructed": true, "context_tool_trim": true, "scan_upstream_request": true, "context_build": true, "context_commit": true, "search_summary": true, "search": true, "upstream_failure": true}
var allowedFields = map[string]bool{"addr": true, "version": true, "message_id": true, "phase": true, "seq": true, "chars": true, "sha256": true, "raw_chars": true, "raw_sha256": true, "http": true, "elapsed_ms": true, "response_bytes": true, "total_ms": true, "reasoning_chars": true, "reasoning_sha256": true, "answer_chars": true, "answer_sha256": true, "ttft_ms": true, "reasoning_ttft_ms": true, "reasoning_duration_ms": true, "answer_ttft_ms": true, "upstream_chunks": true, "reasoning_batches": true, "answer_batches": true, "raw_answer_chars": true, "raw_answer_sha256": true, "bytes": true, "width": true, "height": true, "mime": true, "parse_ms": true, "image_bytes": true, "base64_ms": true, "image_url_chars": true, "tools": true, "final": true, "removed_pairs": true, "ok": true, "canceled": true, "triggered": true, "search_calls": true, "sources": true, "subturns": true, "final_answer_ttft_ms": true, "model_answer_chars": true, "model_answer_sha256": true, "latency_ms": true, "count": true, "body_bytes": true, "stage": true, "kind": true, "upstream_http": true, "response_http": true, "start_utc": true, "received_utc": true}

func sanitize(line string) string {
	i := strings.Index(line, "[LITTLEP-BRIDGE] ")
	if i < 0 {
		return ""
	}
	parts := strings.Fields(line[i+len("[LITTLEP-BRIDGE] "):])
	if len(parts) == 0 || !allowedEvents[parts[0]] {
		return ""
	}
	fields := []string{"bridge", parts[0]}
	for _, p := range parts[1:] {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 || !allowedFields[kv[0]] || !tokenValue.MatchString(kv[1]) {
			continue
		}
		fields = append(fields, p)
	}
	return strings.Join(fields, " ")
}

type logger struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func newLogger(path string) (*logger, error) { l := &logger{path: path}; return l, l.open() }
func (l *logger) open() error {
	f, e := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	l.f = f
	s, _ := f.Stat()
	l.size = s.Size()
	return nil
}
func (l *logger) close() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}
func (l *logger) write(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := time.Now().UTC().Format(time.RFC3339Nano) + " " + s + "\n"
	if l.size+int64(len(line)) > maxLog {
		l.close()
		_ = os.Remove(l.path + ".1")
		_ = os.Rename(l.path, l.path+".1")
		if l.open() != nil {
			return
		}
	}
	n, e := l.f.WriteString(line)
	if e == nil {
		l.size += int64(n)
	}
}
func streamChild(r io.Reader, l *logger, done chan<- struct{}) {
	defer close(done)
	reader := bufio.NewReaderSize(r, 32*1024)
	var line []byte
	discard := false
	for {
		chunk, e := reader.ReadSlice('\n')
		if !discard {
			if len(line)+len(chunk) > 128*1024 {
				discard = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if e == nil {
			if !discard {
				if x := sanitize(string(line)); x != "" {
					l.write(x)
				}
			} else {
				l.write("supervisor oversized_child_line_discarded")
			}
			line = nil
			discard = false
			continue
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if e == io.EOF {
			if len(line) > 0 && !discard {
				if x := sanitize(string(line)); x != "" {
					l.write(x)
				}
			}
			return
		}
		l.write("supervisor child_output_read_failed")
		return
	}
}
func waitOrStop(d time.Duration, stop <-chan os.Signal, c config) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return false
		case <-t.C:
			return enabled(c)
		case <-ticker.C:
			if !enabled(c) {
				return false
			}
		}
	}
}
func run(c config) error {
	if e := os.MkdirAll(c.run, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(c.lock, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("supervisor already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if !enabled(c) {
		return errors.New("disabled")
	}
	if occupied(c) {
		return errors.New("port occupied; refusing bridge launch")
	}
	l, e := newLogger(c.log)
	if e != nil {
		return e
	}
	defer l.close()
	if e = writeIdentity(c.supervisorPID, os.Getpid()); e != nil {
		return e
	}
	defer os.Remove(c.supervisorPID)
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(stop)
	failures := 0
	crashes := []time.Time{}
	state := lifecycle{}
	writeState(c.stateFile, state)
	bootID, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	l.write("supervisor started version=" + version + " reason=" + c.reason + " boot_id=" + strings.TrimSpace(string(bootID)))
	for enabled(c) {
		if occupied(c) {
			l.write("supervisor port_busy; refusing_bridge_launch")
			return errors.New("port occupied")
		}
		cmd := exec.Command(c.bridge, "-config", filepath.Join(c.root, "config.json"))
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		pipe, writer, e := os.Pipe()
		if e != nil {
			return e
		}
		cmd.Stdout = writer
		cmd.Stderr = writer
		began := time.Now()
		if e = cmd.Start(); e != nil {
			pipe.Close()
			writer.Close()
			l.write("supervisor launch_failed")
			return e
		}
		writer.Close()
		if e = writeIdentity(c.childPID, cmd.Process.Pid); e != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			pipe.Close()
			return e
		}
		childIdentity, _ := currentIdentity(cmd.Process.Pid)
		state.LastStart = began.UTC().Format(time.RFC3339Nano)
		state.Failures = failures
		writeState(c.stateFile, state)
		l.write(fmt.Sprintf("supervisor bridge_started pid=%d", cmd.Process.Pid))
		outputDone := make(chan struct{})
		go streamChild(pipe, l, outputDone)
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		var result error
		controlled := false
		observedHealthy := false
		tick := time.NewTicker(250 * time.Millisecond)
	loop:
		for {
			select {
			case result = <-exited:
				break loop
			case <-stop:
				controlled = true
				break loop
			case <-tick.C:
				if !enabled(c) {
					controlled = true
					break loop
				}
				if !observedHealthy {
					var actual string
					observedHealthy, actual = health(c)
					observedHealthy = observedHealthy && actual == bridgeVersion
					if observedHealthy {
						l.write("supervisor bridge_healthy version=" + actual + " addr=" + c.address)
					}
				}
			}
		}
		tick.Stop()
		if controlled {
			if id, ok := readIdentity(c.childPID, c.bridge); ok && id.PID == childIdentity.PID && id.Start == childIdentity.Start {
				_ = syscall.Kill(id.PID, syscall.SIGTERM)
			}
			select {
			case result = <-exited:
			case <-time.After(3 * time.Second):
				if id, ok := readIdentity(c.childPID, c.bridge); ok && id.PID == childIdentity.PID && id.Start == childIdentity.Start {
					_ = syscall.Kill(id.PID, syscall.SIGKILL)
				}
				result = <-exited
			}
		}
		<-outputDone
		pipe.Close()
		_ = os.Remove(c.childPID)
		if controlled {
			l.write("supervisor stopped")
			return nil
		}
		lived := time.Since(began)
		l.write(fmt.Sprintf("supervisor bridge_exited uptime_ms=%d exit=%s", lived.Milliseconds(), exitLabel(result)))
		if observedHealthy && lived >= c.healthyReset {
			failures = 0
			crashes = nil
		}
		now := time.Now()
		kept := crashes[:0]
		for _, x := range crashes {
			if now.Sub(x) <= 60*time.Second {
				kept = append(kept, x)
			}
		}
		crashes = append(kept, now)
		if len(crashes) >= 3 {
			l.write(fmt.Sprintf("supervisor crash_loop count=%d window_s=60", len(crashes)))
		}
		delay := c.backoff[failures]
		if failures < len(c.backoff)-1 {
			failures++
		}
		if len(crashes) >= 3 {
			delay = 30 * time.Second
			failures = len(c.backoff) - 1
		}
		state.Failures = failures
		state.RestartCount++
		writeState(c.stateFile, state)
		l.write(fmt.Sprintf("supervisor restart_backoff seconds=%d", int(delay.Seconds())))
		if !waitOrStop(delay, stop, c) {
			l.write("supervisor stopped")
			return nil
		}
	}
	l.write("supervisor disabled")
	return nil
}
func exitLabel(e error) string {
	if e == nil {
		return "0"
	}
	if x, ok := e.(*exec.ExitError); ok {
		return strconv.Itoa(x.ExitCode())
	}
	return "unknown"
}
func changeFlag(c config, disable bool) error {
	if disable {
		return os.WriteFile(c.disabled, []byte("disabled by r7 lifecycle\n"), 0600)
	}
	e := os.Remove(c.disabled)
	if os.IsNotExist(e) {
		return nil
	}
	return e
}
func stop(c config) error {
	exe, _ := os.Executable()
	id, ok := readIdentity(c.supervisorPID, exe)
	if !ok {
		return nil
	}
	if e := syscall.Kill(id.PID, syscall.SIGTERM); e != nil {
		return e
	}
	for i := 0; i < 60; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, ok = readIdentity(c.supervisorPID, exe); !ok {
			return nil
		}
	}
	return errors.New("supervisor did not stop within 6 seconds")
}
func launch(c config) error {
	if !enabled(c) {
		return errors.New("disabled; run enable")
	}
	// Serialize the launch handshake as well as the daemon lifetime. Otherwise
	// concurrent availability probes can mistake each other's temporary bind
	// for a foreign listener before the daemon has published its identity.
	if e := os.MkdirAll(c.run, 0700); e != nil {
		return e
	}
	guard, e := os.OpenFile(c.lock+".launch", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer guard.Close()
	if e = syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(guard.Fd()), syscall.LOCK_UN)
	if !enabled(c) {
		return errors.New("disabled; run enable")
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	if _, ok := readIdentity(c.supervisorPID, exe); ok {
		return nil
	}
	if occupied(c) {
		return errors.New("port occupied; refusing bridge launch")
	}
	if e := os.MkdirAll(c.run, 0700); e != nil {
		return e
	}
	cmd := exec.Command(exe, "run", c.reason)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	null, e := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if e != nil {
		return e
	}
	defer null.Close()
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	if e = cmd.Start(); e != nil {
		return e
	}
	_ = cmd.Process.Release()
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, ok := readIdentity(c.supervisorPID, exe); !ok {
			continue
		}
		if child, valid := readIdentity(c.childPID, c.bridge); valid && child.PID > 1 {
			if h, v := health(c); h && v == bridgeVersion {
				return nil
			}
		}
	}
	if _, ok := readIdentity(c.supervisorPID, exe); ok {
		return errors.New("supervisor running, but bridge not healthy within 3 seconds; inspect status and bridge.log")
	}
	return errors.New("supervisor did not start; inspect bridge.log")
}
func main() {
	c := settings()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: littlep-supervisor {run|start|stop|restart|status|disable|enable}")
		os.Exit(2)
	}
	c.reason = "manual"
	var e error
	switch os.Args[1] {
	case "run":
		if len(os.Args) > 2 && (os.Args[2] == "boot" || os.Args[2] == "manual" || os.Args[2] == "restart" || os.Args[2] == "enable") {
			c.reason = os.Args[2]
		}
		e = run(c)
	case "start":
		if len(os.Args) > 2 && os.Args[2] == "boot" {
			c.reason = "boot"
		}
		e = launch(c)
		if e == nil {
			printStatus(c)
		}
	case "stop":
		e = changeFlag(c, true)
		if e == nil {
			e = stop(c)
		}
		if e == nil {
			printStatus(c)
		}
	case "restart":
		c.reason = "restart"
		e = stop(c)
		if e == nil {
			e = changeFlag(c, false)
		}
		if e == nil {
			e = launch(c)
		}
		if e == nil {
			printStatus(c)
		}
	case "status":
		printStatus(c)
	case "disable":
		e = changeFlag(c, true)
		if e == nil {
			e = stop(c)
		}
		if e == nil {
			printStatus(c)
		}
	case "enable":
		c.reason = "enable"
		e = changeFlag(c, false)
		if e == nil {
			e = launch(c)
		}
		if e == nil {
			printStatus(c)
		}
	default:
		e = errors.New("unknown command")
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "r7:", e)
		os.Exit(1)
	}
}

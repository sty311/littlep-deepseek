# Architecture

The bridge is a Go standard-library-only service. `bridge/cmd/` contains executable entry points; `bridge/internal/bridge/` separates incoming multipart parsing, DeepSeek messages/streaming, SSE adaptation, search, tool orchestration, session storage/context budgeting, formula display compatibility, health and configuration.

CaptureFrame, stitching and ASR remain vendor components. The native patch compares the exact chat path before returning the loopback base; other requests keep their original URL. The authored page wrapper intercepts live `reasoningText`, delegates ordinary answer/history rendering to a locally renamed original component, and strips search-control packets before display/hashing.

The supervisor is Linux-only and separate from model logic. No rootfs, global TLS, DNS or audio changes are required. Original/renamed vendor modules are never distributed here. See [protocol](protocol.md), [autostart](autostart.md) and [patch recipe](../patch/README.md).

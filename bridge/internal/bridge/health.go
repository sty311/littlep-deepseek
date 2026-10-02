package bridge

import (
	"encoding/json"
	"net/http"
)

const version = "0.7.0-source-ready"

func newHandler(client deepSeekClient, providers ...SearchProvider) http.Handler {
	var provider SearchProvider
	if len(providers) > 0 {
		provider = providers[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version, "web_search": provider != nil})
	})
	b := &bridge{client: client, busy: make(chan struct{}, 1), search: provider, sessions: NewSessionStore()}
	mux.HandleFunc(chatPath, b.serveChat)
	mux.HandleFunc("/debug/sessions", b.serveSessionDebug)
	return mux
}

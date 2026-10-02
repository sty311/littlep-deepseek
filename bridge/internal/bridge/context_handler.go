package bridge

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
)

func (b *bridge) contextMessages(input Input, encoder *youdaoEncoder) ([]chatMessage, error) {
	snapshot := b.sessions.Snapshot(encoder.chatID)
	messages, stats, err := BuildContextMessagesWithStats(snapshot, input, encoder.messageID)
	encoded, _ := json.Marshal(stats)
	log.Printf("[LITTLEP-BRIDGE] context_build message_id=%s stats=%s ok=%t", encoder.messageID, encoded, err == nil)
	return messages, err
}

// Called only after upstream DONE and a successful terminal downstream flush.
// The raw final sub-turn is passed explicitly, never the accumulated UI text.
func (b *bridge) commitContext(ctx context.Context, input Input, encoder *youdaoEncoder, answer string, sources []SearchSource) {
	err := b.sessions.Commit(ctx, encoder.chatID, input, answer, sources)
	log.Printf("[LITTLEP-BRIDGE] context_commit message_id=%s ok=%t canceled=%t", encoder.messageID, err == nil, ctx.Err() != nil)
}

func (b *bridge) serveSessionDebug(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(b.sessions.DebugSessions())
}

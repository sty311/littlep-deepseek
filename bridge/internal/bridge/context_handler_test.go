package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func contextRequest(t *testing.T, id, question string) *http.Request {
	text, _ := json.Marshal([]any{map[string]any{"type": "text", "text": map[string]string{"content": question}}})
	return multipartChat(t, map[string]string{"chatId": id, "messageScene": "dayiPracticeAsk", "messageContents": string(text)})
}

func returnedChatID(t *testing.T, wire string) string {
	for _, event := range parseWireEvents(t, wire) {
		if event.Event != "begin" {
			continue
		}
		_, item := payloadItem(event.Data)
		if item != nil && item["type"] == "chat" {
			return item["chat"].(map[string]any)["chatId"].(string)
		}
	}
	t.Fatal("missing chat ID in begin")
	return ""
}

func TestContextHTTPGeneratedIDContinuesAndNewIDIsolated(t *testing.T) {
	for _, withSearch := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "search-enabled"}[withSearch], func(t *testing.T) {
			var received []map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload := decodeModelRequest(t, r)
				received = append(received, payload)
				if payload["max_tokens"] != float64(16000) {
					t.Error("output limit missing")
				}
				streamFrame(w, map[string]any{"reasoning_content": "private-current-reasoning"}, nil)
				streamFrame(w, map[string]any{"content": "final-only-answer"}, nil)
				streamFrame(w, map[string]any{}, "stop")
				endToolStream(w)
			}))
			defer upstream.Close()
			var handler http.Handler
			if withSearch {
				handler = newHandler(testChatClient(upstream), &stubSearch{})
			} else {
				handler = newHandler(testChatClient(upstream))
			}
			first := newFlushRecorder()
			handler.ServeHTTP(first, contextRequest(t, "", "第一问"))
			id := returnedChatID(t, first.Body.String())
			second := newFlushRecorder()
			handler.ServeHTTP(second, contextRequest(t, id, "那第二问呢"))
			if returnedChatID(t, second.Body.String()) != id {
				t.Fatal("changed session ID")
			}
			history := received[1]["messages"].([]any)
			if len(history) != 3 || history[0].(map[string]any)["content"] != "第一问" || history[1].(map[string]any)["content"] != "final-only-answer" {
				t.Fatalf("history=%v", history)
			}
			for _, raw := range history {
				msg := raw.(map[string]any)
				if msg["role"] == "system" || msg["reasoning_content"] != nil || msg["tool_calls"] != nil {
					t.Fatalf("history contamination: %v", msg)
				}
			}
			third := newFlushRecorder()
			handler.ServeHTTP(third, contextRequest(t, "", "新话题"))
			if returnedChatID(t, third.Body.String()) == id || len(received[2]["messages"].([]any)) != 1 {
				t.Fatal("new session leaked history")
			}
		})
	}
}

func TestContextSearchCommitKeepsOnlyFinalContent(t *testing.T) {
	var received []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, decodeModelRequest(t, r))
		switch len(received) {
		case 1:
			streamFrame(w, map[string]any{"reasoning_content": "tool-only-reasoning"}, nil)
			streamFrame(w, map[string]any{"content": "interim-search-preamble"}, nil)
			streamFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, "id-search", "function", "web_search", `{"query":"news"}`)}}, nil)
			streamFrame(w, map[string]any{}, "tool_calls")
		default:
			streamFrame(w, map[string]any{"reasoning_content": "current-only-reasoning"}, nil)
			streamFrame(w, map[string]any{"content": "final-news-answer"}, nil)
			streamFrame(w, map[string]any{}, "stop")
		}
		endToolStream(w)
	}))
	defer upstream.Close()
	provider := &stubSearch{search: func(context.Context, string) (*SearchResult, error) {
		return &SearchResult{Sources: []SearchSource{{Title: "Page", URL: "https://example.com", Snippet: "fact"}}}, nil
	}}
	handler := newHandler(testChatClient(upstream), provider)
	first := newFlushRecorder()
	handler.ServeHTTP(first, contextRequest(t, "", "最新情况"))
	id := returnedChatID(t, first.Body.String())
	second := newFlushRecorder()
	handler.ServeHTTP(second, contextRequest(t, id, "再说明一下"))
	history := received[2]["messages"].([]any)
	if len(history) != 3 || history[1].(map[string]any)["content"] != "final-news-answer" {
		t.Fatalf("not clean final history: %v", history)
	}
	raw, _ := json.Marshal(history)
	for _, forbidden := range []string{"reasoning_content", "tool_calls", "interim-search-preamble", "external_untrusted_reference_data", "来源", "MYAI4"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("historical leak: %s", forbidden)
		}
	}
	continuation := received[1]["messages"].([]any)[1].(map[string]any)
	if continuation["reasoning_content"] != "tool-only-reasoning" || continuation["tool_calls"] == nil {
		t.Fatal("current tool loop lost full assistant")
	}
}

type cancelFinishWriter struct {
	*flushRecorder
	cancel context.CancelFunc
}

func (w *cancelFinishWriter) Write(p []byte) (int, error) {
	n, err := w.flushRecorder.Write(p)
	if strings.Contains(string(p), "[DONE]") {
		w.cancel()
	}
	return n, err
}

func TestContextTerminalCancelDoesNotCommit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamFrame(w, map[string]any{"content": "not-committed"}, nil)
		streamFrame(w, map[string]any{}, "stop")
		endToolStream(w)
	}))
	defer upstream.Close()
	store := NewSessionStore()
	b := &bridge{client: testChatClient(upstream), busy: make(chan struct{}, 1), sessions: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &cancelFinishWriter{newFlushRecorder(), cancel}
	b.serveChat(w, contextRequest(t, "cancel-test", "q").WithContext(ctx))
	if len(store.Snapshot("cancel-test").Pairs) != 0 {
		t.Fatal("canceled terminal output committed")
	}
}

func TestContextDebugIsSummaryAndLoopbackOnly(t *testing.T) {
	store := NewSessionStore()
	if err := store.Commit(context.Background(), "visible-id", Input{Question: "private-question"}, "private-answer", nil); err != nil {
		t.Fatal(err)
	}
	b := &bridge{sessions: store}
	r := httptest.NewRequest("GET", "/debug/sessions", nil)
	r.RemoteAddr = "127.0.0.1:1000"
	w := httptest.NewRecorder()
	b.serveSessionDebug(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "visible-id") || strings.Contains(w.Body.String(), "private-") {
		t.Fatal(w.Body.String())
	}
	r.RemoteAddr = "192.168.0.1:1000"
	w = httptest.NewRecorder()
	b.serveSessionDebug(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("non-loopback debug permitted")
	}
}

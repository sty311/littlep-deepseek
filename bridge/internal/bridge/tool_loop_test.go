package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubSearch struct {
	mu      sync.Mutex
	queries []string
	search  func(context.Context, string) (*SearchResult, error)
}

func (s *stubSearch) Search(ctx context.Context, query string) (*SearchResult, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	return s.search(ctx, query)
}

func (s *stubSearch) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

func toolFrame(w http.ResponseWriter, delta map[string]any, finish any) {
	streamFrame(w, delta, finish)
}

func endToolStream(w http.ResponseWriter) {
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

func toolDelta(index int, id, kind, name, arguments string) map[string]any {
	return map[string]any{"index": index, "id": id, "type": kind, "function": map[string]string{"name": name, "arguments": arguments}}
}

func testChatClient(server *httptest.Server) deepSeekClient {
	return deepSeekClient{url: server.URL, key: "test-key", model: "deepseek-flash", http: server.Client()}
}

func decodeModelRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestToolDeltaAssemblyAndCompleteAssistantTurn(t *testing.T) {
	var wire bytes.Buffer
	writeFrame := func(delta map[string]any, finish any) {
		frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(&wire, "data: %s\n\n", frame)
	}
	writeFrame(map[string]any{"reasoning_content": "consider search"}, nil)
	writeFrame(map[string]any{"content": "I will check."}, nil)
	writeFrame(map[string]any{"tool_calls": []any{toolDelta(1, "id-2", "function", "web_search", `{"query":"second`), toolDelta(0, "id-1", "function", "web_search", `{"query":"first`)}}, nil)
	writeFrame(map[string]any{"tool_calls": []any{toolDelta(0, "", "", "", ` query"}`), toolDelta(1, "", "", "", ` query"}`)}}, nil)
	writeFrame(map[string]any{}, "tool_calls")
	wire.WriteString("data: [DONE]\n\n")
	deltas := make(chan deepSeekDelta, 8)
	outcome, err := (deepSeekClient{}).readStream(context.Background(), &wire, deltas)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.FinishReason != "tool_calls" || outcome.Message.Role != "assistant" || outcome.Message.Reasoning != "consider search" || outcome.Message.Content != "I will check." {
		t.Fatalf("assistant turn was not preserved: %+v", outcome)
	}
	if len(outcome.Message.ToolCalls) != 2 || outcome.Message.ToolCalls[0].ID != "id-1" || outcome.Message.ToolCalls[0].Function.Arguments != `{"query":"first query"}` || outcome.Message.ToolCalls[1].ID != "id-2" || outcome.Message.ToolCalls[1].Function.Arguments != `{"query":"second query"}` {
		t.Fatalf("tool deltas assembled incorrectly: %+v", outcome.Message.ToolCalls)
	}
	// The next model request must replay every field of the assistant turn,
	// including visible content when a tool-producing turn contains it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeModelRequest(t, r)
		messages := request["messages"].([]any)
		assistant := messages[1].(map[string]any)
		if assistant["reasoning_content"] != "consider search" || assistant["content"] != "I will check." || len(assistant["tool_calls"].([]any)) != 2 {
			t.Errorf("assistant fields lost on continuation: %v", assistant)
		}
		tool := messages[2].(map[string]any)
		if tool["tool_call_id"] != "id-1" || tool["content"] != "search result" {
			t.Errorf("tool result id lost: %v", tool)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := testChatClient(server)
	stream, err := client.openMessages(context.Background(), []chatMessage{{Role: "user", Content: "question"}, outcome.Message, {Role: "tool", ToolCallID: "id-1", Content: "search result"}}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	stream.Body.Close()
}

func TestToolLoopPreservesTurnAndToolIDWithFooterAndControl(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeModelRequest(t, r)
		mu.Lock()
		requests = append(requests, request)
		turn := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			toolFrame(w, map[string]any{"reasoning_content": "need current sources"}, nil)
			toolFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, "call-1", "function", "web_search", `{"query":"latest news"}`)}}, nil)
			toolFrame(w, map[string]any{}, "tool_calls")
		} else {
			toolFrame(w, map[string]any{"reasoning_content": "sources found"}, nil)
			toolFrame(w, map[string]any{"content": "Final answer."}, nil)
			toolFrame(w, map[string]any{}, "stop")
		}
		endToolStream(w)
	}))
	defer server.Close()
	sources := make([]SearchSource, 6)
	for i := range sources {
		sources[i] = SearchSource{URL: fmt.Sprintf("https://example.org/%d", i), Title: fmt.Sprintf("Title %d", i)}
	}
	provider := &stubSearch{search: func(ctx context.Context, query string) (*SearchResult, error) {
		return &SearchResult{Summary: "retrieved", Sources: sources}, nil
	}}
	recorder := newFlushRecorder()
	newHandler(testChatClient(server), provider).ServeHTTP(recorder, chatInput(t))
	if !strings.Contains(recorder.Body.String(), "event:end\ndata:[DONE]") || provider.count() != 1 {
		t.Fatalf("incomplete tool loop; calls=%d body=%s", provider.count(), recorder.Body.String())
	}
	mu.Lock()
	if len(requests) != 2 {
		mu.Unlock()
		t.Fatalf("model turns=%d", len(requests))
	}
	second := requests[1]
	mu.Unlock()
	messageList := second["messages"].([]any)
	if len(messageList) != 3 {
		t.Fatalf("continued messages = %v", messageList)
	}
	assistant := messageList[1].(map[string]any)
	if assistant["role"] != "assistant" || assistant["reasoning_content"] != "need current sources" || assistant["content"] != "" || assistant["tool_calls"].([]any)[0].(map[string]any)["id"] != "call-1" {
		t.Fatalf("assistant tool turn was not replayed fully: %v", assistant)
	}
	tool := messageList[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call-1" || !strings.Contains(tool["content"].(string), `"ok":true`) {
		t.Fatalf("tool result or id missing: %v", tool)
	}
	events := parseWireEvents(t, recorder.Body.String())
	var answer, normalReasoning, control string
	for _, event := range events {
		if event.Event != "message" || event.Data["code"] != float64(0) {
			continue
		}
		_, item := payloadItem(event.Data)
		if item == nil || item["type"] != "text" {
			continue
		}
		text := item["text"].(map[string]any)
		if text["type"] == "text" {
			answer += text["content"].(string)
		} else if text["type"] == "reasoningText" {
			if text["content"] != "" {
				t.Fatal("status/control entered reasoning text.content and TTS")
			}
			value := text["content_latex"].(string)
			if strings.Contains(value, controlStart) {
				control += value
			} else {
				normalReasoning += value
			}
		}
	}
	if !strings.Contains(control, `"state":"init"`) || !strings.Contains(control, `"state":"searching"`) || !strings.Contains(control, `"state":"complete"`) || strings.Contains(answer, controlStart) || strings.Contains(normalReasoning, controlStart) {
		t.Fatalf("status packets leaked or missing: answer=%q reasoning=%q control=%q", answer, normalReasoning, control)
	}
	if !strings.Contains(answer, "Final answer.") || strings.Count(answer, "example.org\n") != 5 || strings.Contains(answer, "Title 5") || strings.Contains(answer, "https://example.org/") {
		t.Fatalf("source footer must cap at five: %q", answer)
	}
}

func TestToolLoopMixedContentAndToolCallsRemainStreamed(t *testing.T) {
	var mu sync.Mutex
	var replay map[string]any
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeModelRequest(t, r)
		mu.Lock()
		turn++
		current := turn
		if current == 2 {
			replay = request
		}
		mu.Unlock()
		if current == 1 {
			toolFrame(w, map[string]any{"reasoning_content": "Planning."}, nil)
			toolFrame(w, map[string]any{"content": "I'll search now."}, nil)
			toolFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, "mixed-call", "function", "web_search", `{"query":"today's weather"}`)}}, nil)
			toolFrame(w, map[string]any{}, "tool_calls")
		} else {
			toolFrame(w, map[string]any{"reasoning_content": "Checking sources."}, nil)
			toolFrame(w, map[string]any{"content": "Final answer."}, nil)
			toolFrame(w, map[string]any{}, "stop")
		}
		endToolStream(w)
	}))
	defer server.Close()
	provider := &stubSearch{search: func(ctx context.Context, query string) (*SearchResult, error) {
		return &SearchResult{Sources: []SearchSource{{URL: "https://weather.example/today", Title: "Weather report"}}}, nil
	}}
	var logs bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldLogWriter)
	recorder := newFlushRecorder()
	newHandler(testChatClient(server), provider).ServeHTTP(recorder, chatInput(t))
	if provider.count() != 1 || !strings.Contains(recorder.Body.String(), "event:end\ndata:[DONE]") {
		t.Fatalf("mixed turn failed: searches=%d body=%s", provider.count(), recorder.Body.String())
	}
	mu.Lock()
	second := replay
	mu.Unlock()
	if second == nil {
		t.Fatal("model continuation missing")
	}
	messages := second["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("continuation messages = %v", messages)
	}
	assistant := messages[1].(map[string]any)
	if assistant["reasoning_content"] != "Planning." || assistant["content"] != "I'll search now." || assistant["tool_calls"].([]any)[0].(map[string]any)["id"] != "mixed-call" {
		t.Fatalf("mixed assistant turn was not fully replayed: %v", assistant)
	}
	tool := messages[2].(map[string]any)
	if tool["tool_call_id"] != "mixed-call" || !strings.Contains(tool["content"].(string), "weather.example/today") {
		t.Fatalf("search tool result omitted full source URL: %v", tool)
	}
	events := parseWireEvents(t, recorder.Body.String())
	var answer, reasoning string
	var answerEvents int
	for _, event := range events {
		if event.Event != "message" || event.Data["code"] != float64(0) {
			continue
		}
		_, item := payloadItem(event.Data)
		if item == nil || item["type"] != "text" {
			continue
		}
		value := item["text"].(map[string]any)
		if value["type"] == "text" {
			answer += value["content"].(string)
			answerEvents++
		} else if value["type"] == "reasoningText" && !strings.Contains(value["content_latex"].(string), controlStart) {
			reasoning += value["content_latex"].(string)
		}
	}
	if answerEvents < 3 || !strings.HasPrefix(answer, "I'll search now.\n\nFinal answer.") || !strings.Contains(answer, "Weather report\nweather.example\n") || reasoning != "Planning.Checking sources." {
		t.Fatalf("mixed turn output order or resumed reasoning failed: answer=%q reasoning=%q events=%d", answer, reasoning, answerEvents)
	}
	modelHash := sha256.Sum256([]byte("Final answer."))
	displayHash := sha256.Sum256([]byte(answer))
	if !strings.Contains(logs.String(), fmt.Sprintf("model_answer_sha256=%x", modelHash)) || !strings.Contains(logs.String(), fmt.Sprintf("answer_sha256=%x", displayHash)) || modelHash == displayHash {
		t.Fatalf("model/display answer hashes were not separated: %s", logs.String())
	}
}

func TestToolLoopThreeSearchesThenToolChoiceNone(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeModelRequest(t, r)
		mu.Lock()
		requests = append(requests, request)
		turn := len(requests)
		mu.Unlock()
		if turn <= 3 {
			toolFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, fmt.Sprintf("call-%d", turn), "function", "web_search", fmt.Sprintf(`{"query":"q%d"}`, turn))}}, nil)
			toolFrame(w, map[string]any{}, "tool_calls")
		} else {
			toolFrame(w, map[string]any{"content": "Done."}, nil)
			toolFrame(w, map[string]any{}, "stop")
		}
		endToolStream(w)
	}))
	defer server.Close()
	provider := &stubSearch{search: func(ctx context.Context, query string) (*SearchResult, error) {
		return &SearchResult{Sources: []SearchSource{{URL: "https://example.org/" + query}}}, nil
	}}
	recorder := newFlushRecorder()
	newHandler(testChatClient(server), provider).ServeHTTP(recorder, chatInput(t))
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 || provider.count() != 3 || requests[3]["tool_choice"] != "none" || requests[3]["tools"] == nil || !strings.Contains(recorder.Body.String(), "event:end\ndata:[DONE]") {
		t.Fatalf("limit or final tool choice failed: turns=%d searches=%d final=%v", len(requests), provider.count(), requests[len(requests)-1])
	}
}

func TestToolLoopNoSearchStreamsAndDisabledHandlerOmitsTools(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("search_enabled=%t", enabled), func(t *testing.T) {
			var sawTools bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := decodeModelRequest(t, r)
				sawTools = request["tools"] != nil
				toolFrame(w, map[string]any{"content": "First "}, nil)
				time.Sleep(100 * time.Millisecond)
				toolFrame(w, map[string]any{"content": "second."}, nil)
				toolFrame(w, map[string]any{}, "stop")
				endToolStream(w)
			}))
			defer server.Close()
			provider := &stubSearch{search: func(ctx context.Context, query string) (*SearchResult, error) {
				t.Fatal("provider was called without a model tool call")
				return nil, nil
			}}
			var handler http.Handler
			if enabled {
				handler = newHandler(testChatClient(server), provider)
			} else {
				handler = newHandler(testChatClient(server))
			}
			recorder := newFlushRecorder()
			handler.ServeHTTP(recorder, chatInput(t))
			if sawTools != enabled || provider.count() != 0 || recorder.flushes < 3 || !strings.Contains(recorder.Body.String(), "event:end\ndata:[DONE]") {
				t.Fatalf("no-search path changed: enabled=%t tools=%t calls=%d flushes=%d", enabled, sawTools, provider.count(), recorder.flushes)
			}
		})
	}
}

func TestToolLoopSearchFailureFallback(t *testing.T) {
	var second map[string]any
	var mu sync.Mutex
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeModelRequest(t, r)
		mu.Lock()
		turn++
		current := turn
		if current == 2 {
			second = request
		}
		mu.Unlock()
		if current == 1 {
			toolFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, "failed-call", "function", "web_search", `{"query":"today"}`)}}, nil)
			toolFrame(w, map[string]any{}, "tool_calls")
		} else {
			toolFrame(w, map[string]any{"content": "Search unavailable."}, nil)
			toolFrame(w, map[string]any{}, "stop")
		}
		endToolStream(w)
	}))
	defer server.Close()
	provider := &stubSearch{search: func(ctx context.Context, query string) (*SearchResult, error) {
		return nil, errors.New("private upstream secret")
	}}
	recorder := newFlushRecorder()
	newHandler(testChatClient(server), provider).ServeHTTP(recorder, chatInput(t))
	mu.Lock()
	got := second
	mu.Unlock()
	if got == nil || !strings.Contains(recorder.Body.String(), "event:end\ndata:[DONE]") || strings.Contains(recorder.Body.String(), "private upstream secret") {
		t.Fatal("search failure did not safely fall back")
	}
	tools := got["messages"].([]any)
	tool := tools[len(tools)-1].(map[string]any)
	if tool["tool_call_id"] != "failed-call" || !strings.Contains(tool["content"].(string), `"ok":false`) || strings.Contains(tool["content"].(string), "private upstream secret") {
		t.Fatalf("unsafe fallback tool result: %v", tool)
	}
}

func TestToolLoopCancelsSearchWithRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toolFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, "cancel-call", "function", "web_search", `{"query":"slow"}`)}}, nil)
		toolFrame(w, map[string]any{}, "tool_calls")
		endToolStream(w)
	}))
	defer server.Close()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	provider := &stubSearch{search: func(ctx context.Context, query string) (*SearchResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	request := chatInput(t).WithContext(ctx)
	recorder := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		newHandler(testChatClient(server), provider).ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("search did not begin")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("search did not receive request cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tool loop did not exit after cancellation")
	}
	if strings.Contains(recorder.Body.String(), "event:end\ndata:[DONE]") {
		t.Fatal("cancelled search completed successfully")
	}
}

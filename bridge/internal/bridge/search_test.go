package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testSearchProvider(t *testing.T, server *httptest.Server) *DeepSeekNativeSearchProvider {
	t.Helper()
	p, err := NewDeepSeekNativeSearchProvider(server.URL+"/anthropic/v1", "test-secret", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNativeSearchWireAndStructuredResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/anthropic/v1/messages" {
			t.Errorf("method/path = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-secret" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("incorrect headers: %v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "deepseek-flash" || body["max_tokens"] != float64(4096) || body["system"] != nil || body["stream"] != nil {
			t.Errorf("unexpected request fields: %v", body)
		}
		tools, _ := body["tools"].([]any)
		if len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search_20250305" || tools[0].(map[string]any)["name"] != "web_search" || tools[0].(map[string]any)["max_uses"] != float64(3) {
			t.Errorf("unexpected tools: %v", tools)
		}
		messages, _ := body["messages"].([]any)
		if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
			t.Errorf("unexpected messages: %v", messages)
		} else {
			content := messages[0].(map[string]any)["content"].([]any)
			if len(content) != 1 || content[0].(map[string]any)["text"] != "Perform a web search for the query: Mars rover" {
				t.Errorf("unexpected content: %v", content)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"content":[
			{"type":"text","text":"A summary with https://not-a-source.example in prose.","citations":[{"url":"https://a.example/one","cited_text":"first snippet"},{"url":"https://a.example/one","cited_text":"later snippet"},{"url":"https://b.example/two","cited_text":"second snippet"}]},
			{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://a.example/one","title":"One","page_age":"2 days ago"},{"type":"web_search_result","url":"https://a.example/one","title":"Duplicate"},{"type":"web_search_result","url":"ftp://bad.example/","title":"Bad"},{"type":"other","url":"https://ignored.example/"},{"type":"web_search_result","url":"https://b.example/two","title":"Two"}]}
		]}`)
	}))
	defer server.Close()
	p := testSearchProvider(t, server)
	result, err := p.Search(context.Background(), "  Mars  \n rover  ")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "A summary with https://not-a-source.example in prose." || len(result.Sources) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got := result.Sources[0]; got.URL != "https://a.example/one" || got.Title != "One" || got.PageAge != "2 days ago" || got.Snippet != "first snippet" {
		t.Fatalf("first source = %+v", got)
	}
	if result.Sources[1].Snippet != "second snippet" {
		t.Fatalf("second source = %+v", result.Sources[1])
	}
}

func TestNativeSearchSourceCapAndNoStructuredResults(t *testing.T) {
	var items []map[string]string
	for i := 0; i < 10; i++ {
		items = append(items, map[string]string{"type": "web_search_result", "url": fmt.Sprintf("https://example.org/%d", i)})
	}
	data, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "web_search_tool_result", "content": items}}})
	result, err := parseDeepSeekSearchResponse(data)
	if err != nil || len(result.Sources) != 8 {
		t.Fatalf("source cap: result=%+v err=%v", result, err)
	}
	for _, payload := range []string{`{"content":[{"type":"text","text":"https://example.org/"}]}`, `{"content":[{"type":"web_search_tool_result","content":[]}]}`, `{invalid`} {
		if result, err := parseDeepSeekSearchResponse([]byte(payload)); err == nil || result != nil {
			t.Fatalf("accepted no results: result=%+v err=%v", result, err)
		}
	}
}

func TestNativeSearchErrorsAreRedactedAndNotCached(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"test-secret and private upstream details"}}`)
	}))
	defer server.Close()
	p := testSearchProvider(t, server)
	for i := 0; i < 2; i++ {
		_, err := p.Search(context.Background(), "query")
		if err == nil || strings.Contains(err.Error(), "test-secret") || strings.Contains(err.Error(), "private upstream") || !strings.Contains(err.Error(), "401") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("failure was cached: calls=%d", calls.Load())
	}
}

func TestNativeSearchCacheAndCopyIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"content":[{"type":"web_search_tool_result","content":[{"type":"web_search_result","url":"https://example.org/a","title":"Original"}]},{"type":"text","text":"summary"}]}`)
	}))
	defer server.Close()
	p := testSearchProvider(t, server)
	first, err := p.Search(context.Background(), "Some  query")
	if err != nil {
		t.Fatal(err)
	}
	first.Sources[0].Title = "Mutated"
	second, err := p.Search(context.Background(), " some query ")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || second.Sources[0].Title != "Original" {
		t.Fatalf("cache missed or exposed mutable slice: calls=%d second=%+v", calls.Load(), second)
	}
}

func TestNativeSearchCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	p := testSearchProvider(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Search(ctx, "slow query")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("search did not reach upstream")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("search ignored cancellation")
	}
}

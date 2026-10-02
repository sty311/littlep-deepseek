package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeepSeekStreamingRequestAndParser(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected route")
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("authorization absent")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 6 || string(body["max_tokens"]) != "16000" || body["extra_body"] != nil || body["tools"] != nil {
			t.Errorf("unexpected keys: %v", body)
		}
		var stream bool
		var effort, model string
		var thinking struct {
			Type string `json:"type"`
		}
		var messages []struct{ Role, Content string }
		_ = json.Unmarshal(body["stream"], &stream)
		_ = json.Unmarshal(body["reasoning_effort"], &effort)
		_ = json.Unmarshal(body["model"], &model)
		_ = json.Unmarshal(body["thinking"], &thinking)
		_ = json.Unmarshal(body["messages"], &messages)
		if !stream || effort != "high" || model != "deepseek-flash" || thinking.Type != "enabled" ||
			len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "  测试问题\n" {
			t.Errorf("invalid request payload")
		}
		_, _ = io.WriteString(w, ": keep-alive\n\ndata: {\"choices\":[{\"delta\":{\"reasoning_content\":\" 思考\",\"content\":null},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"过程\",\"content\":\"答案\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"total_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := deepSeekClient{url: upstream.URL + "/chat/completions", key: "test-key", model: "deepseek-flash", http: &http.Client{Timeout: time.Second}}
	stream, err := client.openStream(context.Background(), "  测试问题\n")
	if err != nil || stream.HTTPStatus != 200 {
		t.Fatalf("open: %v status=%d", err, stream.HTTPStatus)
	}
	defer stream.Body.Close()
	deltas := make(chan deepSeekDelta, 8)
	stats, err := client.readStream(context.Background(), stream.Body, deltas)
	if err != nil || stats.Chunks != 4 || stats.FinishReason != "stop" {
		t.Fatalf("parse: %+v %v", stats, err)
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas=%d", len(deltas))
	}
	first, second := <-deltas, <-deltas
	if first.Reasoning != " 思考" || first.Answer != "" || second.Reasoning != "过程" || second.Answer != "答案" {
		t.Fatalf("fields were merged: %+v %+v", first, second)
	}
}

func TestDeepSeekRedactsKeyFromUpstreamError(t *testing.T) {
	const key = "private-test-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid private-test-key"}}`)
	}))
	defer upstream.Close()
	client := deepSeekClient{url: upstream.URL, key: key, model: "deepseek-flash", http: upstream.Client()}
	_, err := client.openStream(context.Background(), "hello")
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("upstream error was not redacted: %v", err)
	}
}

func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	newHandler(deepSeekClient{}).ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), version) {
		t.Fatal("health failed")
	}
}

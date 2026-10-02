package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func multipartChat(t *testing.T, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, chatPath, &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return r
}

func chatInput(t *testing.T) *http.Request {
	return multipartChat(t, map[string]string{
		"messageScene":    "dayiPracticeAsk",
		"chatId":          "existing-chat",
		"messageContents": `[{"type":"text","text":{"content":"什么是氧化还原反应？"}}]`,
	})
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
	times   []time.Time
	flushed chan struct{}
}

func (w *flushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.flushes++
	w.times = append(w.times, time.Now())
	if w.flushed != nil {
		select {
		case w.flushed <- struct{}{}:
		default:
		}
	}
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{}, 2)}
}

func streamFrame(w http.ResponseWriter, delta map[string]any, finish any) {
	payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	w.(http.Flusher).Flush()
}

func parseWireEvents(t *testing.T, body string) []struct {
	Event string
	Data  map[string]any
} {
	t.Helper()
	raw := strings.Split(strings.TrimSpace(body), "\n\n")
	events := make([]struct {
		Event string
		Data  map[string]any
	}, 0, len(raw))
	for _, part := range raw {
		lines := strings.Split(part, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event:") || !strings.HasPrefix(lines[1], "data:") {
			t.Fatalf("bad SSE frame: %q", part)
		}
		event := struct {
			Event string
			Data  map[string]any
		}{Event: strings.TrimPrefix(lines[0], "event:")}
		if event.Event != "end" {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data:")), &event.Data); err != nil {
				t.Fatal(err)
			}
		}
		events = append(events, event)
	}
	return events
}

func payloadItem(event map[string]any) (map[string]any, map[string]any) {
	data := event["data"].(map[string]any)
	meta, _ := data["msg"].(map[string]any)
	list, _ := data["list"].([]any)
	if len(list) == 0 {
		return meta, nil
	}
	return meta, list[0].(map[string]any)
}

func TestBridgeSlowStreamingBatchesAndHashes(t *testing.T) {
	reasonPieces := make([]string, 16)
	answerPieces := make([]string, 11)
	for i := range reasonPieces {
		reasonPieces[i] = strings.Repeat("思", 100)
	}
	for i := range answerPieces {
		answerPieces[i] = strings.Repeat("答", 50)
	}
	reason, answer := strings.Join(reasonPieces, ""), strings.Join(answerPieces, "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		streamFrame(w, map[string]any{}, nil)
		for _, part := range reasonPieces {
			streamFrame(w, map[string]any{"reasoning_content": part}, nil)
			time.Sleep(35 * time.Millisecond)
		}
		for _, part := range answerPieces {
			streamFrame(w, map[string]any{"content": part}, nil)
			time.Sleep(35 * time.Millisecond)
		}
		streamFrame(w, map[string]any{}, "stop")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	client := deepSeekClient{url: upstream.URL, key: "test-secret", model: "deepseek-flash", http: upstream.Client()}
	output := newFlushRecorder()
	newHandler(client).ServeHTTP(output, chatInput(t))
	if output.Code != 200 || output.flushes < 8 {
		t.Fatalf("status=%d flushes=%d", output.Code, output.flushes)
	}
	events := parseWireEvents(t, output.Body.String())
	if events[0].Event != "begin" || events[len(events)-1].Event != "end" {
		t.Fatal("missing begin/end")
	}
	_, beginItem := payloadItem(events[0].Data)
	if beginItem["type"] != "chat" {
		t.Fatal("missing chat begin")
	}
	beginMeta, _ := payloadItem(events[0].Data)
	id := beginMeta["messageId"]
	var gotReason, gotAnswer string
	progressCount, reasonBatches, answerBatches, doneCount := 0, 0, 0, 0
	answerStarted := false
	for _, event := range events[1 : len(events)-1] {
		if event.Event != "message" {
			t.Fatalf("unexpected event %s", event.Event)
		}
		meta, item := payloadItem(event.Data)
		if item == nil {
			doneCount++
			continue
		}
		if meta["messageId"] != id {
			t.Fatal("unstable messageId")
		}
		switch item["type"] {
		case "dayiProgress":
			if progressCount != 0 || answerStarted || item["dayiProgress"].(map[string]any)["type"] != "reasoningSummary" {
				t.Fatal("bad reasoning seed")
			}
			progressCount++
		case "text":
			text := item["text"].(map[string]any)
			if text["type"] == "reasoningText" {
				if answerStarted || text["content"] != "" {
					t.Fatal("reasoning reached TTS or came after answer")
				}
				gotReason += text["content_latex"].(string)
				reasonBatches++
			} else if text["type"] == "text" {
				answerStarted = true
				if text["content_latex"] != text["content"] {
					t.Fatal("plain answer changed in the display adapter")
				}
				gotAnswer += text["content"].(string)
				answerBatches++
			} else {
				t.Fatal("unexpected text type")
			}
		default:
			t.Fatal("unexpected item type")
		}
	}
	if gotReason != reason || gotAnswer != answer || progressCount != 1 || reasonBatches < 2 || answerBatches < 2 || doneCount != 1 {
		t.Fatalf("bad reconstructed stream: reason=%d answer=%d seed=%d batches=%d/%d done=%d", len([]rune(gotReason)), len([]rune(gotAnswer)), progressCount, reasonBatches, answerBatches, doneCount)
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("reasoning_sha256=%x", sha256.Sum256([]byte(reason)))) ||
		!strings.Contains(logs.String(), fmt.Sprintf("answer_sha256=%x", sha256.Sum256([]byte(answer)))) ||
		!strings.Contains(logs.String(), "message_id="+id.(string)) ||
		!strings.Contains(logs.String(), "ttft_ms=") || !strings.Contains(logs.String(), "reasoning_ttft_ms=") || !strings.Contains(logs.String(), "answer_ttft_ms=") ||
		!strings.Contains(logs.String(), "upstream_chunks=") || strings.Contains(logs.String(), "test-secret") ||
		strings.Contains(output.Body.String(), "test-secret") {
		t.Fatal("log metadata or redaction failed")
	}
}

func TestBridgeMixedFieldsAndNoReasoning(t *testing.T) {
	for _, withReason := range []bool{true, false} {
		t.Run(fmt.Sprintf("reason=%v", withReason), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				delta := map[string]any{"content": "答案"}
				if withReason {
					delta["reasoning_content"] = "思考"
				}
				streamFrame(w, delta, nil)
				streamFrame(w, map[string]any{}, "stop")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer upstream.Close()
			output := newFlushRecorder()
			newHandler(deepSeekClient{url: upstream.URL, key: "x", model: "deepseek-flash", http: upstream.Client()}).ServeHTTP(output, chatInput(t))
			body := output.Body.String()
			if !strings.Contains(body, "event:end\ndata:[DONE]") {
				t.Fatal("missing success end")
			}
			if withReason {
				if strings.Index(body, "reasoningText") >= strings.Index(body, "\"content\":\"答案\"") {
					t.Fatal("mixed fields reordered")
				}
			} else if strings.Contains(body, "reasoningText") || strings.Contains(body, "dayiProgress") {
				t.Fatal("empty reasoning produced seed")
			}
		})
	}
}

func TestBridgeRejectsIncompleteAbnormalAndLateReasoning(t *testing.T) {
	cases := map[string]func(http.ResponseWriter){
		"missing_done": func(w http.ResponseWriter) { streamFrame(w, map[string]any{"content": "部分答案"}, "stop") },
		"malformed_json": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, "data: {invalid-json}\n\n")
		},
		"done_without_stop": func(w http.ResponseWriter) {
			streamFrame(w, map[string]any{"content": "未结束答案"}, nil)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		},
		"length": func(w http.ResponseWriter) {
			streamFrame(w, map[string]any{"content": "截断答案"}, "length")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		},
		"late_reason": func(w http.ResponseWriter) {
			streamFrame(w, map[string]any{"content": "答案"}, nil)
			streamFrame(w, map[string]any{"reasoning_content": "非法"}, nil)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		},
		"delta_after_stop": func(w http.ResponseWriter) {
			streamFrame(w, map[string]any{"content": "答案"}, "stop")
			streamFrame(w, map[string]any{"content": "非法续写"}, nil)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		},
		"repeated_finish": func(w http.ResponseWriter) {
			streamFrame(w, map[string]any{"content": "答案"}, "stop")
			streamFrame(w, map[string]any{}, "length")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		},
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { script(w) }))
			defer upstream.Close()
			output := newFlushRecorder()
			newHandler(deepSeekClient{url: upstream.URL, key: "x", model: "deepseek-flash", http: upstream.Client()}).ServeHTTP(output, chatInput(t))
			body := output.Body.String()
			if strings.Contains(body, "event:end") || strings.Contains(body, "\"newChatStatus\"") || strings.Contains(body, "upstream request failed") {
				t.Fatalf("failure was misreported as SSE success: %s", body)
			}
			events := parseWireEvents(t, body)
			last := events[len(events)-1]
			if last.Event != "message" || last.Data["code"] != float64(1) {
				t.Fatalf("missing native error callback frame: %v", last)
			}
		})
	}
}

func TestBridgeDownstreamCancelCancelsUpstream(t *testing.T) {
	upstreamCancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamFrame(w, map[string]any{"reasoning_content": "进行中"}, nil)
		<-r.Context().Done()
		close(upstreamCancelled)
	}))
	defer upstream.Close()
	requestContext, cancel := context.WithCancel(context.Background())
	request := chatInput(t).WithContext(requestContext)
	output := newFlushRecorder()
	completed := make(chan struct{})
	go func() {
		newHandler(deepSeekClient{url: upstream.URL, key: "x", model: "deepseek-flash", http: upstream.Client()}).ServeHTTP(output, request)
		close(completed)
	}()
	select {
	case <-output.flushed:
	case <-time.After(time.Second):
		t.Fatal("no flushed begin")
	}
	cancel()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop on cancel")
	}
	select {
	case <-upstreamCancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not cancelled")
	}
	if strings.Contains(output.Body.String(), "event:end") || strings.Contains(output.Body.String(), "\"newChatStatus\"") {
		t.Fatal("cancelled stream completed")
	}
}

func TestBridgeRejectsUnadaptedImage(t *testing.T) {
	request := multipartChat(t, map[string]string{"messageScene": "dayiPracticeScan", "messageContents": `[{"type":"image","image":{"url":"x"}}]`})
	response := httptest.NewRecorder()
	newHandler(deepSeekClient{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatal("image without an uploaded JPEG was accepted")
	}
}

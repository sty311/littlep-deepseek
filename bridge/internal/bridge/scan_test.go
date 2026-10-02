package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// Synthetic unit fixture only; this is not a captured Little P scan.
func syntheticJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 231, G: 27, B: 83, A: 255})
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 93}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

const scanContents = `[{"type":"image","image":{"idx":"0","lineType":"single","imageSize":"66,834","patterns":"subjects","format":"jpg"}}]`

type scanFile struct {
	name, filename, mime string
	data                 []byte
}

func scanRequest(t *testing.T, fields [][2]string, files ...scanFile) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		headers := textproto.MIMEHeader{}
		headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, file.name, file.filename))
		headers.Set("Content-Type", file.mime)
		part, err := writer.CreatePart(headers)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.data); err != nil {
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

func scanFields() [][2]string {
	return [][2]string{{"messageScene", "dayiPracticeScan"}, {"messageContents", scanContents}}
}

func jpegFile(data []byte) scanFile {
	return scanFile{name: scanFilePartName, filename: "scan.jpg", mime: "image/jpeg", data: data}
}

func TestScanSyntheticJPEGExactBytesAndTextPreserved(t *testing.T) {
	original := syntheticJPEG(t)
	input, err := parseChatRequest(httptest.NewRecorder(), scanRequest(t, scanFields(), jpegFile(original)))
	if err != nil || input.Scan == nil {
		t.Fatalf("scan parse: %v", err)
	}
	if input.Question != defaultScanQuestion || input.Scan.MIME != "image/jpeg" || input.Scan.Width != 3 || input.Scan.Height != 2 || input.Scan.SHA256 != sha256.Sum256(original) || !bytes.Equal(input.Scan.Bytes, original) {
		t.Fatalf("scan changed original bytes or metadata: %+v", input.Scan)
	}
	packed, err := json.Marshal(buildDeepSeekMessages(input))
	if err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL    string `json:"url"`
				Detail string `json:"detail"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(packed, &messages); err != nil || len(messages) != 1 || len(messages[0].Content) != 2 {
		t.Fatalf("multimodal JSON: %s (%v)", packed, err)
	}
	parts := messages[0].Content
	if parts[0].Type != "text" || parts[0].Text != defaultScanQuestion || parts[1].Type != "image_url" || parts[1].ImageURL.Detail != "original" {
		t.Fatalf("wrong content parts: %+v", parts)
	}
	const prefix = "data:image/jpeg;base64,"
	if !strings.HasPrefix(parts[1].ImageURL.URL, prefix) {
		t.Fatal("JPEG data URL absent")
	}
	roundTrip, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(parts[1].ImageURL.URL, prefix))
	if err != nil || !bytes.Equal(roundTrip, original) || sha256.Sum256(roundTrip) != input.Scan.SHA256 {
		t.Fatal("base64 roundtrip changed JPEG bytes")
	}
	withText := append(scanFields(), [2]string{"chatId", "chat-1"})
	withText[1][1] = `[{"type":"text","text":{"content":"  请读图\n"}},` + strings.TrimPrefix(scanContents, "[")
	textInput, err := parseChatRequest(httptest.NewRecorder(), scanRequest(t, withText, jpegFile(original)))
	if err != nil || textInput.Question != "  请读图\n" || textInput.ChatID != "chat-1" {
		t.Fatalf("attached text changed: %q %v", textInput.Question, err)
	}
	plain, err := parseChatRequest(httptest.NewRecorder(), chatInput(t))
	if err != nil || plain.Scan != nil || plain.Question != "什么是氧化还原反应？" {
		t.Fatalf("text path changed: %+v %v", plain, err)
	}
	plainJSON, _ := json.Marshal(buildDeepSeekMessages(plain))
	if !bytes.Contains(plainJSON, []byte(`"content":"什么是氧化还原反应？"`)) {
		t.Fatalf("text content no longer string: %s", plainJSON)
	}
}

func TestScanRejectsMissingMalformedOversizeDuplicatesAndPaths(t *testing.T) {
	valid := syntheticJPEG(t)
	base := scanFields()
	cases := []struct {
		name   string
		fields [][2]string
		files  []scanFile
	}{
		{"missing_file", base, nil},
		{"path_value", append(scanFields(), [2]string{scanFilePartName, `C:\private\scan.jpg`}), nil},
		{"malformed_jpeg", base, []scanFile{jpegFile([]byte{0xff, 0xd8, 0xff, 0xd9})}},
		{"wrong_bytes", base, []scanFile{jpegFile([]byte("not a JPEG"))}},
		{"duplicate_file", base, []scanFile{jpegFile(valid), jpegFile(valid)}},
		{"duplicate_contents", append(scanFields(), [2]string{"messageContents", scanContents}), []scanFile{jpegFile(valid)}},
		{"oversized_file", base, []scanFile{jpegFile(bytes.Repeat([]byte{0xff}, maxImageBytes+1))}},
		{"metadata_without_image", [][2]string{{"messageScene", "dayiPracticeScan"}, {"messageContents", `[{"type":"image","image":{"idx":"0","format":"jpg"}}]`}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseChatRequest(httptest.NewRecorder(), scanRequest(t, tc.fields, tc.files...))
			if err == nil {
				t.Fatal("invalid image request accepted")
			}
			if tc.name == "oversized_file" && inputStatus(err) != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversized image status=%d: %v", inputStatus(err), err)
			}
		})
	}
	// Content-Type and filename are untrusted labels. Actual JPEG bytes win;
	// no filesystem path is ever opened, including a traversal filename.
	for _, label := range []scanFile{
		{scanFilePartName, "../secret.jpg", "application/octet-stream", valid},
		{scanFilePartName, "scan.png", "image/png", valid},
	} {
		input, err := parseChatRequest(httptest.NewRecorder(), scanRequest(t, scanFields(), label))
		if err != nil || input.Scan == nil || !bytes.Equal(input.Scan.Bytes, valid) {
			t.Fatalf("JPEG bytes must be read from uploaded part only: %v", err)
		}
	}
	manyFields := scanFields()
	for i := 0; i < 63; i++ {
		manyFields = append(manyFields, [2]string{fmt.Sprintf("unused_%d", i), "x"})
	}
	_, err := parseChatRequest(httptest.NewRecorder(), scanRequest(t, manyFields, jpegFile(valid)))
	if err == nil || inputStatus(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("multipart part count limit missing: %v", err)
	}
}

func TestScanImageSurvivesToolFollowup(t *testing.T) {
	original := syntheticJPEG(t)
	wantHash := sha256.Sum256(original)
	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldLog)
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		requests = append(requests, payload)
		if len(requests) == 1 {
			streamFrame(w, map[string]any{"reasoning_content": "图像推理"}, nil)
			streamFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, "image-search", "function", "web_search", `{"query":"example"}`)}}, nil)
			streamFrame(w, map[string]any{}, "tool_calls")
		} else {
			streamFrame(w, map[string]any{"content": "图像答案"}, nil)
			streamFrame(w, map[string]any{}, "stop")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider := &stubSearch{search: func(context.Context, string) (*SearchResult, error) {
		return &SearchResult{Summary: "example"}, nil
	}}
	response := newFlushRecorder()
	newHandler(testChatClient(server), provider).ServeHTTP(response, scanRequest(t, scanFields(), jpegFile(original)))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event:end\ndata:[DONE]") || len(requests) != 2 || provider.count() != 1 {
		t.Fatalf("scan tool loop failed: status=%d turns=%d", response.Code, len(requests))
	}
	for index, request := range requests {
		messages := request["messages"].([]any)
		parts, ok := messages[0].(map[string]any)["content"].([]any)
		if !ok || len(parts) != 2 {
			t.Fatalf("turn %d lost image parts", index)
		}
		imagePart := parts[1].(map[string]any)["image_url"].(map[string]any)
		url := imagePart["url"].(string)
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/jpeg;base64,"))
		if err != nil || sha256.Sum256(decoded) != wantHash || imagePart["detail"] != "original" {
			t.Fatalf("turn %d changed image", index)
		}
	}
	continued := requests[1]["messages"].([]any)
	assistant := continued[1].(map[string]any)
	if assistant["reasoning_content"] != "图像推理" || assistant["tool_calls"].([]any)[0].(map[string]any)["id"] != "image-search" || continued[2].(map[string]any)["tool_call_id"] != "image-search" {
		t.Fatal("assistant reasoning, tool call, or tool reply was not replayed")
	}
	events := parseWireEvents(t, response.Body.String())
	meta, _ := payloadItem(events[0].Data)
	id := meta["messageId"].(string)
	if !strings.Contains(logs.String(), "scan_multimodal_constructed message_id="+id) || strings.Count(logs.String(), "scan_upstream_request message_id="+id) != 2 || !strings.Contains(logs.String(), fmt.Sprintf("sha256=%x", wantHash)) || strings.Contains(logs.String(), "data:image/jpeg;base64,") {
		t.Fatal("image metrics lost message ID/hash or logged base64")
	}
}

func TestScanSearchEnabledUpstreamFailureTypes(t *testing.T) {
	original := syntheticJPEG(t)
	cases := []struct {
		name                            string
		status, wantStatus              int
		errorMessage, wantMessage, kind string
	}{
		{"vision_invalid", 400, 400, "invalid image", "模型无法处理这张图片", "vision_invalid"},
		{"vision_unsupported", 400, 400, "Vision unsupported on this model", "模型暂不支持图片输入", "vision_unsupported"},
		{"oversize", 413, 413, "payload too large", "图片超过模型大小限制", "image_oversize"},
		{"rate_limit", 429, 429, "rate limited", "模型繁忙，请稍后再试", "rate_limit"},
		{"service", 503, 502, "private-service-detail", "模型服务暂不可用", "model_service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": tc.errorMessage}})
			}))
			defer server.Close()
			var logs bytes.Buffer
			oldLog := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(oldLog)
			provider := &stubSearch{search: func(context.Context, string) (*SearchResult, error) {
				t.Fatal("search should not run before model response")
				return nil, nil
			}}
			response := newFlushRecorder()
			newHandler(testChatClient(server), provider).ServeHTTP(response, scanRequest(t, scanFields(), jpegFile(original)))
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), tc.wantMessage) || strings.Contains(response.Body.String(), tc.errorMessage) || strings.Contains(response.Body.String(), "event:begin") || !strings.Contains(logs.String(), "kind="+tc.kind) || strings.Contains(logs.String(), tc.errorMessage) {
				t.Fatalf("failure classification: status=%d body=%s logs=%s", response.Code, response.Body.String(), logs.String())
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(80 * time.Millisecond)
		}))
		defer server.Close()
		client := testChatClient(server)
		client.http = &http.Client{Timeout: 10 * time.Millisecond}
		var logs bytes.Buffer
		oldLog := log.Writer()
		log.SetOutput(&logs)
		defer log.SetOutput(oldLog)
		provider := &stubSearch{search: func(context.Context, string) (*SearchResult, error) { return nil, nil }}
		response := newFlushRecorder()
		newHandler(client, provider).ServeHTTP(response, scanRequest(t, scanFields(), jpegFile(original)))
		if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "模型请求超时") || !strings.Contains(logs.String(), "kind=timeout") {
			t.Fatalf("timeout classification: status=%d body=%s logs=%s", response.Code, response.Body.String(), logs.String())
		}
	})
}

func TestScanSearchEnabledFollowupAndStreamFailures(t *testing.T) {
	original := syntheticJPEG(t)
	for _, mode := range []string{"followup_429", "malformed_stream"} {
		t.Run(mode, func(t *testing.T) {
			turn := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turn++
				if turn == 1 && mode == "followup_429" {
					streamFrame(w, map[string]any{"tool_calls": []any{toolDelta(0, "search-1", "function", "web_search", `{"query":"example"}`)}}, nil)
					streamFrame(w, map[string]any{}, "tool_calls")
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
					return
				}
				if mode == "followup_429" {
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"message":"private-rate-detail"}}`)
					return
				}
				_, _ = io.WriteString(w, "data: {bad-json}\n\n")
			}))
			defer server.Close()
			provider := &stubSearch{search: func(context.Context, string) (*SearchResult, error) {
				return &SearchResult{Summary: "ok"}, nil
			}}
			var logs bytes.Buffer
			oldLog := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(oldLog)
			response := newFlushRecorder()
			newHandler(testChatClient(server), provider).ServeHTTP(response, scanRequest(t, scanFields(), jpegFile(original)))
			if response.Code != 200 || strings.Contains(response.Body.String(), "event:end") || !strings.Contains(response.Body.String(), `"code":1`) || strings.Contains(response.Body.String(), "private-rate-detail") {
				t.Fatalf("native failure envelope missing: status=%d body=%s", response.Code, response.Body.String())
			}
			if mode == "followup_429" {
				if !strings.Contains(response.Body.String(), "模型繁忙，请稍后再试") || !strings.Contains(logs.String(), "kind=rate_limit upstream_http=429") {
					t.Fatalf("followup error lost: %s", logs.String())
				}
			} else if !strings.Contains(response.Body.String(), "模型输出中断") || !strings.Contains(logs.String(), "kind=model_stream") {
				t.Fatalf("stream error lost: %s", logs.String())
			}
		})
	}
}

func TestScanNoSearchAndUpstreamErrorTypes(t *testing.T) {
	original := syntheticJPEG(t)
	for _, status := range []int{200, 400, 413, 429, 500} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request["tools"] != nil || len(request["messages"].([]any)[0].(map[string]any)["content"].([]any)) != 2 {
					t.Error("no-search scan payload lost image or gained tools")
				}
				if status != 200 {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"message":"private-model-detail"}}`)
					return
				}
				streamFrame(w, map[string]any{"content": "图像答案"}, nil)
				streamFrame(w, map[string]any{}, "stop")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			client := testChatClient(server)
			response := newFlushRecorder()
			newHandler(client).ServeHTTP(response, scanRequest(t, scanFields(), jpegFile(original)))
			wantStatus := status
			if status == 500 {
				wantStatus = 502
			}
			if response.Code != wantStatus || strings.Contains(response.Body.String(), "private-model-detail") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if status == 200 && !strings.Contains(response.Body.String(), "event:end\ndata:[DONE]") {
				t.Fatal("no-search scan did not complete")
			}
		})
	}
}

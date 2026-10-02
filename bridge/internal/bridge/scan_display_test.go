package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func displayFromParts(parts ...string) (string, string) {
	adapter := newScanDisplayAdapter()
	var raw, display strings.Builder
	for _, part := range parts {
		r, d := adapter.consume(part)
		raw.WriteString(r)
		display.WriteString(d)
	}
	r, d := adapter.finish()
	raw.WriteString(r)
	display.WriteString(d)
	return raw.String(), display.String()
}

func TestSyntheticScanDisplayEveryByteBoundary(t *testing.T) {
	source := `synthetic \(x^2\), \[\sqrt{2}\], and \[\ce{Fe^{3+} + e- -> Fe^{2+}}\]`
	raw, display := displayFromParts(source)
	if raw != source || !strings.Contains(display, "<latex ") {
		t.Fatal("synthetic formula conversion failed")
	}
	for cut := 0; cut <= len(source); cut++ {
		a, b := displayFromParts(source[:cut], source[cut:])
		if a != raw || b != display {
			t.Fatalf("split %d changed result", cut)
		}
	}
}

func TestScanDisplayCodeAndMalformedFallback(t *testing.T) {
	source := "`\\(x\\)` and ``\\[y\\]``\n   ````go\n\\[z\\]\n```\n\\(q\\)\n   ````\n\\(r\\) and \\$5 then $x$"
	raw, display := displayFromParts(source)
	if raw != source || strings.Count(display, "<latex ") != 2 || !strings.Contains(display, `\(q\)`) || !strings.Contains(display, `\[z\]`) || !strings.Contains(display, `value="$r$"`) || !strings.Contains(display, `\$5`) {
		t.Fatalf("code span/fence handling failed: %q", display)
	}
	for _, source := range []string{`prefix \[unfinished`, `\[` + strings.Repeat("x", maxPendingScanFormula) + `\]`, `\[` + strings.Repeat("x", maxPendingScanFormula+1), "末尾$"} {
		raw, display := displayFromParts(source)
		if raw != source || display != source {
			t.Fatalf("fallback changed raw source of length %d", len(source))
		}
	}
	_, escaped := displayFromParts(`\[a&b <c> "q" 's'\]`)
	if !strings.Contains(escaped, `a&#38;b &#60;c&#62; &#34;q&#34; &#x27;s&#x27;`) || strings.Contains(escaped, `value="$a&b`) {
		t.Fatalf("unsafe formula attribute: %q", escaped)
	}
}

func TestScanDisplaySSEKeepsRawAndHashesDisplay(t *testing.T) {
	modelAnswer := "答案：\\[\n\\mathrm{NH_3+H^+=NH_4^+}\n\\] 完毕"
	provider := &stubSearch{search: func(_ context.Context, _ string) (*SearchResult, error) {
		t.Fatal("unexpected search")
		return nil, nil
	}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ch := range modelAnswer {
			streamFrame(w, map[string]any{"content": string(ch)}, nil)
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
	output := newFlushRecorder()
	newHandler(testChatClient(upstream), provider).ServeHTTP(output, scanRequest(t, scanFields(), jpegFile(syntheticJPEG(t))))
	if output.Code != 200 {
		t.Fatalf("http %d: %s", output.Code, output.Body.String())
	}
	var raw, display strings.Builder
	for _, event := range parseWireEvents(t, output.Body.String()) {
		if event.Event != "message" {
			continue
		}
		_, item := payloadItem(event.Data)
		if item == nil || item["type"] != "text" {
			continue
		}
		value := item["text"].(map[string]any)
		if value["type"] == "text" {
			raw.WriteString(value["content"].(string))
			display.WriteString(value["content_latex"].(string))
		}
	}
	if raw.String() != modelAnswer || strings.Count(display.String(), "<latex ") != 1 || strings.Contains(display.String(), `\[`) {
		t.Fatalf("raw/display mismatch: %q / %q", raw.String(), display.String())
	}
	if !strings.Contains(logs.String(), "answer_sha256="+hexDigest(display.String())) || !strings.Contains(logs.String(), "raw_answer_sha256="+hexDigest(raw.String())) {
		t.Fatalf("display/raw hashes missing: %s", logs.String())
	}
}

func hexDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum)
}

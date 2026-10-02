package bridge

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func imagePart(t *testing.T, message chatMessage) contentPart {
	t.Helper()
	if len(message.ContentParts) != 2 || message.ContentParts[1].ImageURL == nil {
		t.Fatalf("missing original image part: %+v", message)
	}
	return message.ContentParts[1]
}

func TestBuildContextTwoTextTurns(t *testing.T) {
	store := NewSessionStore()
	commitTurn(t, store, "chat", "第一问", "第一答", nil, nil)
	messages, stats, err := BuildContextMessagesWithStats(store.Snapshot("chat"), Input{Question: "第二问"}, "m2")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[0].Content != "第一问" || messages[1].Role != "assistant" || messages[1].Content != "第一答" || messages[2].Role != "user" || messages[2].Content != "第二问" {
		t.Fatalf("bad text context: %+v", messages)
	}
	if stats.IncludedPairs != 1 || stats.EstimatedInput > stats.Budget {
		t.Fatalf("bad stats: %+v", stats)
	}
	for _, message := range messages {
		if message.Reasoning != "" || len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.Role == "system" || message.Role == "tool" {
			t.Fatalf("ephemeral or privileged message leaked: %+v", message)
		}
	}
}

func TestBuildContextRetainsOriginalSHAAndNewScanSuppressesOld(t *testing.T) {
	store := NewSessionStore()
	original := []byte("jpeg-original-exact")
	scan := &ScanInput{Bytes: original, MIME: "image/jpeg", SHA256: sha256.Sum256(original), Width: 32, Height: 16}
	commitTurn(t, store, "scan", "图一", "图一解", scan, nil)
	messages, stats, err := BuildContextMessagesWithStats(store.Snapshot("scan"), Input{Question: "继续解释"}, "followup")
	if err != nil || len(messages) != 3 || !stats.PinnedImage {
		t.Fatalf("image not pinned: %v %+v", err, stats)
	}
	part := imagePart(t, messages[0])
	if part.ImageSHA256 != scan.SHA256 || part.ImageBytes != len(original) {
		t.Fatal("image provenance lost")
	}
	encoded := strings.TrimPrefix(part.ImageURL.URL, "data:image/jpeg;base64,")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != string(original) {
		t.Fatal("image was changed or reencoded")
	}
	newImage := []byte("jpeg-new-exact")
	newScan := &ScanInput{Bytes: newImage, MIME: "image/jpeg", SHA256: sha256.Sum256(newImage)}
	messages, stats, err = BuildContextMessagesWithStats(store.Snapshot("scan"), Input{Question: "新图", Scan: newScan}, "new")
	if err != nil || stats.PinnedImage || len(messages) != 3 || len(messages[0].ContentParts) != 0 {
		t.Fatalf("old image replayed into new scan: %v %+v %+v", err, stats, messages)
	}
	if imagePart(t, messages[2]).ImageSHA256 != newScan.SHA256 {
		t.Fatal("new scan absent from current user message")
	}
}

func TestBuildContextNumberedSourceAndGenericSources(t *testing.T) {
	store := NewSessionStore()
	sources := make([]SearchSource, 8)
	for i := range sources {
		sources[i] = SearchSource{URL: fmt.Sprintf("https://source.test/%d", i+1), Title: fmt.Sprintf("资料%d", i+1), Snippet: fmt.Sprintf("第%d条具体内容", i+1), PageAge: "2026-10-01"}
	}
	commitTurn(t, store, "s", "找资料", "原始模型回答", nil, sources)
	snapshot := store.Snapshot("s")
	messages, stats, err := BuildContextMessagesWithStats(snapshot, Input{Question: "第二个来源具体说什么？"}, "src")
	if err != nil || stats.SourceCount != 1 || len(messages) != 3 {
		t.Fatalf("numbered source context: %v %+v", err, stats)
	}
	if !strings.Contains(messages[1].Content, `"number":2`) || !strings.Contains(messages[1].Content, "第2条具体内容") || strings.Contains(messages[1].Content, `"number":1`) || strings.Contains(messages[1].Content, "第3条具体内容") {
		t.Fatalf("wrong source reference: %q", messages[1].Content)
	}
	if !strings.Contains(messages[1].Content, "external_untrusted_reference_data") || strings.Contains(messages[1].Content, controlStart) {
		t.Fatal("metadata framing is unsafe")
	}
	messages, stats, err = BuildContextMessagesWithStats(snapshot, Input{Question: "刚才的来源有哪些？"}, "all")
	if err != nil || stats.SourceCount != 8 || !strings.Contains(messages[1].Content, `"number":8`) {
		t.Fatalf("generic source follow-up: %v %+v", err, stats)
	}
	messages, stats, err = BuildContextMessagesWithStats(snapshot, Input{Question: "换一道题"}, "plain")
	if err != nil || stats.SourceCount != 0 || strings.Contains(messages[1].Content, "external_untrusted_reference_data") {
		t.Fatal("source data injected into unrelated turn")
	}
	commitTurn(t, store, "s", "第二个来源具体说什么？", "第二条说了这些", nil, nil)
	messages, stats, err = BuildContextMessagesWithStats(store.Snapshot("s"), Input{Question: "它具体怎么说的？"}, "pronoun")
	if err != nil || stats.SourceCount != 1 || !strings.Contains(messages[len(messages)-2].Content, `"number":2`) {
		t.Fatalf("source pronoun lost its resolved reference: %v %+v", err, stats)
	}
}

func TestBuildContextTrimWholePairsInOrderAndPinImage(t *testing.T) {
	image := []byte("jpeg-bytes")
	snapshot := SessionSnapshot{Pairs: []SessionPair{
		{Question: "old0", Answer: strings.Repeat("a", 30_000)},
		{Question: "scan1", Answer: "scan answer", Scan: &ScanInput{Bytes: image, MIME: "image/jpeg", SHA256: sha256.Sum256(image)}},
		{Question: "oversize2", Answer: strings.Repeat("b", 90_000)},
		{Question: "recent3", Answer: strings.Repeat("c", 20_000)},
		{Question: "recent4", Answer: strings.Repeat("d", 20_000)},
	}}
	messages, stats, err := BuildContextMessagesWithStats(snapshot, Input{Question: "now"}, "trim")
	if err != nil {
		t.Fatal(err)
	}
	if !stats.PinnedImage || stats.IncludedPairs != 3 || stats.DroppedPairs != 2 || len(messages) != 7 {
		t.Fatalf("unexpected trim: %+v roles=%d", stats, len(messages))
	}
	if messages[0].ContentParts[0].Text != "scan1" || messages[2].Content != "recent3" || messages[4].Content != "recent4" || messages[6].Content != "now" {
		t.Fatalf("not complete chronological pairs: %+v", messages)
	}
	for i, message := range messages {
		want := "user"
		if i%2 == 1 {
			want = "assistant"
		}
		if message.Role != want {
			t.Fatalf("role %d = %s, want %s", i, message.Role, want)
		}
	}
	if stats.EstimatedInput > stats.Budget {
		t.Fatalf("budget exceeded: %+v", stats)
	}
}

func TestBuildContextOversizeCurrentFails(t *testing.T) {
	_, err := BuildContextMessages(SessionSnapshot{}, Input{Question: strings.Repeat("x", contextInputLimit())}, "large")
	if err == nil {
		t.Fatal("oversize current message was silently truncated")
	}
}

func TestBuildContextKeepsRecentPrefixAndRejectsOversizePinnedImage(t *testing.T) {
	snapshot := SessionSnapshot{Pairs: []SessionPair{
		{Question: "old-small", Answer: "old"},
		{Question: "recent-large", Answer: strings.Repeat("r", contextInputLimit())},
	}}
	messages, stats, err := BuildContextMessagesWithStats(snapshot, Input{Question: "now"}, "m")
	if err != nil || len(messages) != 1 || stats.IncludedPairs != 0 {
		t.Fatalf("older pair was backfilled after recent pair did not fit: %v %+v", err, stats)
	}
	image := []byte("original")
	snapshot = SessionSnapshot{Pairs: []SessionPair{{Question: "pinned", Answer: strings.Repeat("a", 70_000), Scan: &ScanInput{Bytes: image, MIME: "image/jpeg", SHA256: sha256.Sum256(image)}}}}
	if _, err := BuildContextMessages(snapshot, Input{Question: "now"}, "m"); err == nil {
		t.Fatal("oversize pinned image pair was silently discarded")
	}
}

func TestBudgetCurrentTurnPreservesToolChainAndPinnedImage(t *testing.T) {
	image := []byte("original")
	snapshot := SessionSnapshot{Pairs: []SessionPair{
		{Question: "image", Answer: "image answer", Scan: &ScanInput{Bytes: image, MIME: "image/jpeg", SHA256: sha256.Sum256(image)}},
		{Question: "old", Answer: strings.Repeat("o", 15_000)},
		{Question: "recent", Answer: strings.Repeat("r", 5_000)},
	}}
	messages, err := BuildContextMessages(snapshot, Input{Question: "current"}, "tools")
	if err != nil || len(messages) != 7 {
		t.Fatalf("initial context: %v %d", err, len(messages))
	}
	call := toolCall{ID: "call_1", Type: "function", Function: functionCall{Name: "web_search", Arguments: strings.Repeat("a", 4_000)}}
	assistant := chatMessage{Role: "assistant", Reasoning: strings.Repeat("thought", 5_000), ToolCalls: []toolCall{call}}
	tool := chatMessage{Role: "tool", ToolCallID: "call_1", Content: strings.Repeat("result", 3_000)}
	messages = append(messages, assistant, tool)
	trimmed, err := BudgetCurrentTurnMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(trimmed) != 7 || trimmed[0].ContentParts[0].Text != "image" || trimmed[2].Content != "recent" || trimmed[4].Content != "current" {
		t.Fatalf("history trim broke pair order or image: %d", len(trimmed))
	}
	if trimmed[5].Reasoning != assistant.Reasoning || trimmed[5].ToolCalls[0].Function.Arguments != call.Function.Arguments || trimmed[6].Content != tool.Content {
		t.Fatal("current tool sequence was changed")
	}
	mandatoryTooLarge := append(trimmed, chatMessage{Role: "assistant", Reasoning: strings.Repeat("new reasoning", 2_000)})
	if _, err := BudgetCurrentTurnMessages(mandatoryTooLarge); err == nil {
		t.Fatal("tool growth beyond image + current budget was accepted")
	}
}

func TestBudgetCurrentTurnDoesNotBackfillOlderSmallPair(t *testing.T) {
	messages := []chatMessage{
		{Role: "user", Content: "old-small"}, {Role: "assistant", Content: "old"},
		{Role: "user", Content: "recent-large"}, {Role: "assistant", Content: strings.Repeat("r", 40_000)},
		{Role: "user", Content: "now"},
		{Role: "assistant", Reasoning: strings.Repeat("x", 70_000), ToolCalls: []toolCall{{ID: "c", Type: "function", Function: functionCall{Name: "web_search", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c", Content: "result"},
	}
	trimmed, err := BudgetCurrentTurnMessages(messages)
	if err != nil || len(trimmed) != 3 || trimmed[0].Content != "now" || trimmed[1].Reasoning != messages[5].Reasoning || trimmed[2].Role != "tool" {
		t.Fatalf("dynamic trim backfilled older small pair or damaged tool chain: %v %+v", err, trimmed)
	}
}

func TestBudgetCurrentTurnKeepsSourceMetadataAndImage(t *testing.T) {
	image := []byte("original")
	snapshot := SessionSnapshot{
		Pairs: []SessionPair{
			{Question: "scan", Answer: "scan answer", Scan: &ScanInput{Bytes: image, MIME: "image/jpeg", SHA256: sha256.Sum256(image)}},
			{Question: "middle", Answer: strings.Repeat("m", 15_000)},
			{Question: "search", Answer: "model answer"},
		},
		Sources: []SearchSource{{Title: "first", URL: "https://a.test/1", Snippet: "first content"}, {Title: "second", URL: "https://a.test/2", Snippet: "second content"}},
	}
	messages, err := BuildContextMessages(snapshot, Input{Question: "第二个来源具体说什么"}, "source-budget")
	if err != nil || len(messages) != 7 || !messages[5].ContextPinned {
		t.Fatalf("source metadata was not pinned: %v %+v", err, messages)
	}
	messages = append(messages,
		chatMessage{Role: "assistant", Reasoning: strings.Repeat("r", 54_000), ToolCalls: []toolCall{{ID: "c", Type: "function", Function: functionCall{Name: "web_search", Arguments: "{}"}}}},
		chatMessage{Role: "tool", ToolCallID: "c", Content: "tool result"},
	)
	trimmed, err := BudgetCurrentTurnMessages(messages)
	if err != nil || len(trimmed) != 7 || trimmed[0].ContentParts[0].Text != "scan" || !trimmed[3].ContextPinned || !strings.Contains(trimmed[3].Content, `"number":2`) {
		t.Fatalf("source or original image lost under tool growth: %v %+v", err, trimmed)
	}
}

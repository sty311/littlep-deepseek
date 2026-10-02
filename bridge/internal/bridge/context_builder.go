package bridge

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

var (
	MODEL_CONTEXT_LIMIT = 1_000_000
	CONTEXT_BUDGET      = 128_000
	MAX_OUTPUT_TOKENS   = 16_000

	contextToolsReserve  = 8_192
	contextSafetyReserve = 4_096
	contextImageEstimate = 32_768
	contextMessageCost   = 512
)

// Context estimates are deliberately conservative for text: each UTF-8 byte
// counts as one token plus a per-message margin. Image billing cannot be
// derived from original JPEG bytes, so each image gets a fixed 32K allowance.
// This is an admission estimate, not an exact model tokenizer or API guarantee.
func estimatedMessageCost(text string, hasImage bool) int {
	cost := len(text) + contextMessageCost
	if hasImage {
		cost += contextImageEstimate
	}
	return cost
}

func estimatedChatMessageCost(message chatMessage) int {
	textBytes := len(message.Content) + len(message.Reasoning) + len(message.ToolCallID)
	hasImage := false
	if len(message.ContentParts) > 0 {
		textBytes -= len(message.Content) // MarshalJSON uses ContentParts instead.
		for _, part := range message.ContentParts {
			textBytes += len(part.Type) + len(part.Text)
			if part.Type == "image_url" && part.ImageURL != nil {
				hasImage = true
				textBytes += len(part.ImageURL.Detail)
			}
		}
	}
	for _, call := range message.ToolCalls {
		textBytes += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	cost := textBytes + contextMessageCost
	if hasImage {
		cost += contextImageEstimate
	}
	return cost
}

// BudgetCurrentTurnMessages is applied again before every upstream call: tool
// assistant reasoning, tool-call arguments, and tool results are counted as
// they accumulate. It never splits a history pair or the current tool chain.
// A retained image pair is mandatory; if that plus the current turn exceeds
// the conservative input allowance, the caller receives a safe error.
func BudgetCurrentTurnMessages(messages []chatMessage) ([]chatMessage, error) {
	if len(messages) == 0 || messages[0].Role != "user" {
		return nil, errors.New("message history has no current user")
	}
	currentIndex := 0
	for currentIndex+2 < len(messages) {
		assistant := messages[currentIndex+1]
		next := messages[currentIndex+2]
		if assistant.Role != "assistant" || assistant.Reasoning != "" || len(assistant.ToolCalls) > 0 || assistant.ToolCallID != "" || next.Role != "user" {
			break
		}
		currentIndex += 2
	}
	if messages[currentIndex].Role != "user" {
		return nil, errors.New("message history has no current user")
	}
	mandatoryCost := 0
	for _, message := range messages[currentIndex:] {
		mandatoryCost += estimatedChatMessageCost(message)
	}
	limit := contextInputLimit()
	if mandatoryCost > limit {
		return nil, errors.New("current turn and tool sequence exceed context budget")
	}

	pinnedIndex := -1
	sourcePinnedIndex := -1
	for i := 0; i < currentIndex; i += 2 {
		if messages[i+1].ContextPinned {
			sourcePinnedIndex = i
		}
		for _, part := range messages[i].ContentParts {
			if part.Type == "image_url" && part.ImageURL != nil {
				pinnedIndex = i
			}
		}
	}
	if pinnedIndex >= 0 {
		mandatoryCost += estimatedChatMessageCost(messages[pinnedIndex]) + estimatedChatMessageCost(messages[pinnedIndex+1])
	}
	if sourcePinnedIndex >= 0 && sourcePinnedIndex != pinnedIndex {
		mandatoryCost += estimatedChatMessageCost(messages[sourcePinnedIndex]) + estimatedChatMessageCost(messages[sourcePinnedIndex+1])
	}
	if mandatoryCost > limit {
		return nil, errors.New("current tool sequence and pinned history exceed context budget")
	}
	keep := make([]bool, currentIndex/2)
	if pinnedIndex >= 0 {
		keep[pinnedIndex/2] = true
	}
	if sourcePinnedIndex >= 0 {
		keep[sourcePinnedIndex/2] = true
	}
	remaining := limit - mandatoryCost
	for i := currentIndex - 2; i >= 0; i -= 2 {
		if i == pinnedIndex || i == sourcePinnedIndex {
			continue
		}
		cost := estimatedChatMessageCost(messages[i]) + estimatedChatMessageCost(messages[i+1])
		if cost <= remaining {
			keep[i/2] = true
			remaining -= cost
		} else {
			break
		}
	}
	result := make([]chatMessage, 0, len(messages))
	for i := 0; i < currentIndex; i += 2 {
		if keep[i/2] {
			result = append(result, messages[i], messages[i+1])
		}
	}
	result = append(result, messages[currentIndex:]...)
	return result, nil
}

func contextInputLimit() int {
	budget := CONTEXT_BUDGET
	if MODEL_CONTEXT_LIMIT < budget {
		budget = MODEL_CONTEXT_LIMIT
	}
	return budget - MAX_OUTPUT_TOKENS - contextToolsReserve - contextSafetyReserve
}

type sourceFact struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	PageAge string `json:"page_age,omitempty"`
}

var numberedSourceReference = regexp.MustCompile(`第([一二三四五六七八1-8])(?:个|条)?来源|来源(?:第)?([一二三四五六七八1-8])`)

func referencedSourceIndex(question string) int {
	match := numberedSourceReference.FindStringSubmatch(question)
	if match == nil {
		return -1
	}
	digit := match[1]
	if digit == "" {
		digit = match[2]
	}
	for i, character := range []rune("一二三四五六七八") {
		if digit == string(character) {
			return i
		}
	}
	return int(digit[0] - '1')
}

func asksAboutFocusedSource(question string) bool {
	return strings.Contains(question, "它具体怎么说") || strings.Contains(question, "它怎么说") ||
		strings.Contains(question, "它说什么") || strings.Contains(question, "它提到什么")
}

func latestSourceFacts(question string, sources []SearchSource, focus int) (string, int) {
	if len(sources) == 0 {
		return "", 0
	}
	if len(sources) > maxStoredSources {
		sources = sources[:maxStoredSources]
	}
	facts := make([]sourceFact, 0, len(sources))
	selected := referencedSourceIndex(question)
	if selected < 0 && asksAboutFocusedSource(question) && focus > 0 {
		selected = focus - 1
	}
	for i, source := range sources {
		if selected >= 0 && i != selected {
			continue
		}
		facts = append(facts, sourceFact{
			Number: i + 1, Title: boundSourceText(source.Title, 256),
			URL: boundSourceText(source.URL, 2048), Snippet: boundSourceText(source.Snippet, 1024),
			PageAge: boundSourceText(source.PageAge, 64),
		})
	}
	if len(facts) == 0 {
		return "", 0
	}
	data, _ := json.Marshal(struct {
		SearchSources []sourceFact `json:"external_untrusted_reference_data"`
	}{facts})
	return "\n\n" + string(data), len(facts)
}

func asksAboutSources(question string) bool {
	lower := strings.ToLower(question)
	return strings.Contains(lower, "来源") || strings.Contains(lower, "source") ||
		strings.Contains(lower, "引用") || strings.Contains(lower, "链接") || strings.Contains(lower, "网址")
}

type ContextStats struct {
	Budget         int
	EstimatedInput int
	IncludedPairs  int
	DroppedPairs   int
	PinnedImage    bool
	SourceCount    int
}

// BuildContextMessages constructs complete chronological history pairs plus
// the current user message. The newest retained JPEG is pinned ahead of other
// history; a new scan suppresses it for this request. The caller supplies the
// session ID through SessionStore.Snapshot using the real native chat ID.
func BuildContextMessages(snapshot SessionSnapshot, input Input, messageID string) ([]chatMessage, error) {
	messages, _, err := BuildContextMessagesWithStats(snapshot, input, messageID)
	return messages, err
}

func BuildContextMessagesWithStats(snapshot SessionSnapshot, input Input, messageID string) ([]chatMessage, ContextStats, error) {
	stats := ContextStats{Budget: contextInputLimit()}
	if strings.TrimSpace(input.Question) == "" {
		return nil, stats, errors.New("current question is empty")
	}
	limit := contextInputLimit()
	currentCost := estimatedMessageCost(input.Question, input.Scan != nil)
	if currentCost > limit {
		return nil, stats, errors.New("current question and image exceed context budget")
	}
	remaining := limit - currentCost

	pairs := snapshot.Pairs
	selected := make([]bool, len(pairs))
	imageIndex := -1
	if input.Scan == nil {
		for i := range pairs {
			if pairs[i].Scan != nil {
				imageIndex = i
			}
		}
	}

	sourceSuffix := ""
	if asksAboutSources(input.Question) || (snapshot.SourceFocus > 0 && asksAboutFocusedSource(input.Question)) {
		sourceSuffix, stats.SourceCount = latestSourceFacts(input.Question, snapshot.Sources, snapshot.SourceFocus)
	}
	// Reserve source metadata once, regardless of whether it lands on the
	// latest historical assistant or on the current user fallback.
	if len(sourceSuffix) > remaining {
		return nil, stats, errors.New("source metadata exceeds context budget")
	}
	remaining -= len(sourceSuffix)
	pairCost := func(index int) int {
		pair := pairs[index]
		return estimatedMessageCost(pair.Question, index == imageIndex) + estimatedMessageCost(pair.Answer, false)
	}
	validPair := func(index int) bool {
		return strings.TrimSpace(pairs[index].Question) != "" && strings.TrimSpace(pairs[index].Answer) != ""
	}
	if imageIndex >= 0 && validPair(imageIndex) {
		cost := pairCost(imageIndex)
		if cost <= remaining {
			selected[imageIndex] = true
			remaining -= cost
			stats.PinnedImage = true
		} else {
			return nil, stats, errors.New("pinned image turn exceeds context budget")
		}
	} else if imageIndex >= 0 {
		return nil, stats, errors.New("pinned image turn is incomplete")
	}
	for i := len(pairs) - 1; i >= 0; i-- {
		if selected[i] || !validPair(i) {
			continue
		}
		cost := pairCost(i)
		if cost <= remaining {
			selected[i] = true
			remaining -= cost
		} else {
			break
		}
	}

	attachSourcesToAssistant := sourceSuffix != "" && len(pairs) > 0 && selected[len(pairs)-1]
	currentQuestion := input.Question
	if sourceSuffix != "" && !attachSourcesToAssistant {
		currentQuestion += sourceSuffix
	}

	messages := make([]chatMessage, 0, 2*len(pairs)+1)
	for i, pair := range pairs {
		if !selected[i] {
			stats.DroppedPairs++
			continue
		}
		stats.IncludedPairs++
		previous := Input{Question: pair.Question}
		if i == imageIndex {
			previous.Scan = pair.Scan
		}
		messages = append(messages, buildDeepSeekMessages(previous, messageID)...)
		answer := pair.Answer
		if attachSourcesToAssistant && i == len(pairs)-1 {
			answer += sourceSuffix
		}
		messages = append(messages, chatMessage{Role: "assistant", Content: answer, ContextPinned: attachSourcesToAssistant && i == len(pairs)-1})
	}
	current := input
	current.Question = currentQuestion
	messages = append(messages, buildDeepSeekMessages(current, messageID)...)
	stats.EstimatedInput = limit - remaining
	return messages, stats, nil
}

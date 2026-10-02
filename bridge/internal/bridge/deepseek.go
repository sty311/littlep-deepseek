package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type deepSeekClient struct {
	url   string
	key   string
	model string
	http  *http.Client
}

type upstreamHTTPError struct {
	Status  int
	Message string
}

func (e upstreamHTTPError) Error() string {
	return fmt.Sprintf("upstream HTTP %d: %s", e.Status, e.Message)
}

type upstreamFailure struct {
	Status  int
	Kind    string
	Message string
	HTTP    int
}

func classifyUpstreamFailure(err error, scan bool) upstreamFailure {
	var httpErr upstreamHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusBadRequest:
			if scan {
				lower := strings.ToLower(httpErr.Message)
				if (strings.Contains(lower, "vision") || strings.Contains(lower, "image")) && (strings.Contains(lower, "unsupported") || strings.Contains(lower, "not support")) {
					return upstreamFailure{http.StatusBadRequest, "vision_unsupported", "模型暂不支持图片输入", httpErr.Status}
				}
				return upstreamFailure{http.StatusBadRequest, "vision_invalid", "模型无法处理这张图片", httpErr.Status}
			}
			return upstreamFailure{http.StatusBadRequest, "request_invalid", "模型请求无效", httpErr.Status}
		case http.StatusRequestEntityTooLarge:
			return upstreamFailure{http.StatusRequestEntityTooLarge, "image_oversize", "图片超过模型大小限制", httpErr.Status}
		case http.StatusTooManyRequests:
			return upstreamFailure{http.StatusTooManyRequests, "rate_limit", "模型繁忙，请稍后再试", httpErr.Status}
		}
		return upstreamFailure{http.StatusBadGateway, "model_service", "模型服务暂不可用", httpErr.Status}
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return upstreamFailure{http.StatusGatewayTimeout, "timeout", "模型请求超时", 0}
	}
	if errors.Is(err, context.Canceled) {
		return upstreamFailure{http.StatusBadGateway, "canceled", "请求已取消", 0}
	}
	return upstreamFailure{http.StatusBadGateway, "model_stream", "模型输出中断", 0}
}

func upstreamClientError(err error, scan bool) (int, string) {
	failure := classifyUpstreamFailure(err, scan)
	return failure.Status, failure.Message
}

type upstreamStream struct {
	Body       io.ReadCloser
	HTTPStatus int
	Started    time.Time
}

type deepSeekDelta struct {
	Reasoning string
	Answer    string
}

type upstreamResult struct {
	Chunks       int
	FinishReason string
	Message      chatMessage
	FirstAnswer  time.Time
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}
type chatMessage struct {
	ContextPinned bool          `json:"-"`
	Role          string        `json:"role"`
	Content       string        `json:"content"`
	ContentParts  []contentPart `json:"-"`
	Reasoning     string        `json:"reasoning_content,omitempty"`
	ToolCalls     []toolCall    `json:"tool_calls,omitempty"`
	ToolCallID    string        `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type        string    `json:"type"`
	Text        string    `json:"text,omitempty"`
	ImageURL    *imageURL `json:"image_url,omitempty"`
	ImageBytes  int       `json:"-"`
	ImageSHA256 [32]byte  `json:"-"`
	MessageID   string    `json:"-"`
}

type imageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// Keep assistant/tool Content as a string for SSE accumulation and replay.
// Only an initial multimodal user message changes its JSON content shape.
func (m chatMessage) MarshalJSON() ([]byte, error) {
	content := any(m.Content)
	if len(m.ContentParts) > 0 {
		content = m.ContentParts
	}
	return json.Marshal(struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		Reasoning  string     `json:"reasoning_content,omitempty"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}{m.Role, content, m.Reasoning, m.ToolCalls, m.ToolCallID})
}

func buildDeepSeekMessages(input Input, messageID ...string) []chatMessage {
	message := chatMessage{Role: "user", Content: input.Question}
	if input.Scan != nil {
		id := ""
		if len(messageID) > 0 {
			id = messageID[0]
		}
		started := time.Now()
		encoded := base64.StdEncoding.EncodeToString(input.Scan.Bytes)
		encodeDuration := time.Since(started)
		message.ContentParts = []contentPart{
			{Type: "text", Text: input.Question},
			{Type: "image_url", ImageURL: &imageURL{URL: "data:" + input.Scan.MIME + ";base64," + encoded, Detail: "original"}, ImageBytes: len(input.Scan.Bytes), ImageSHA256: input.Scan.SHA256, MessageID: id},
		}
		log.Printf("[LITTLEP-BRIDGE] scan_multimodal_constructed message_id=%s base64_ms=%d image_bytes=%d width=%d height=%d mime=%s sha256=%x", id, encodeDuration.Milliseconds(), len(input.Scan.Bytes), input.Scan.Width, input.Scan.Height, input.Scan.MIME, input.Scan.SHA256)
	}
	return []chatMessage{message}
}

var webSearchTool = map[string]any{"type": "function", "function": map[string]any{
	"name": "web_search", "description": "Search the public web for current or externally verifiable information. Use when the answer depends on recent/current information or information not reliably known from the model alone.",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]string{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false},
}}

func (c deepSeekClient) openStream(ctx context.Context, question string) (upstreamStream, error) {
	return c.openMessages(ctx, []chatMessage{{Role: "user", Content: question}}, false, false)
}

func (c deepSeekClient) openMessages(ctx context.Context, messages []chatMessage, tools, final bool) (upstreamStream, error) {
	started := time.Now()
	bounded, budgetErr := BudgetCurrentTurnMessages(messages)
	if budgetErr != nil {
		return upstreamStream{Started: started}, budgetErr
	}
	if len(bounded) != len(messages) {
		log.Printf("[LITTLEP-BRIDGE] context_tool_trim removed_pairs=%d", (len(messages)-len(bounded))/2)
	}
	messages = bounded
	for _, message := range messages {
		for _, part := range message.ContentParts {
			if part.Type == "image_url" && part.ImageURL != nil {
				// The input digest is logged at parse/construction. A retained
				// content part in every turn proves tool follow-up replay.
				log.Printf("[LITTLEP-BRIDGE] scan_upstream_request message_id=%s start_utc=%s image_bytes=%d mime=image/jpeg sha256=%x image_url_chars=%d tools=%t final=%t", part.MessageID, started.UTC().Format(time.RFC3339Nano), part.ImageBytes, part.ImageSHA256, len(part.ImageURL.URL), tools, final)
			}
		}
	}
	payload := map[string]any{
		"model":            c.model,
		"stream":           true,
		"thinking":         map[string]string{"type": "enabled"},
		"reasoning_effort": "high",
		"max_tokens":       MAX_OUTPUT_TOKENS,
		"messages":         messages,
	}
	if tools {
		payload["tools"] = []any{webSearchTool}
		if final {
			payload["tool_choice"] = "none"
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return upstreamStream{Started: started}, err
	}
	if len(body) > 48<<20 {
		return upstreamStream{Started: started}, errors.New("请求图片超出上游大小限制")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return upstreamStream{Started: started}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return upstreamStream{Started: started}, err
	}
	stream := upstreamStream{Body: resp.Body, HTTPStatus: resp.StatusCode, Started: started}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var parsed struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		response, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		_ = json.Unmarshal(response, &parsed)
		message := strings.ReplaceAll(parsed.Error.Message, c.key, "[redacted]")
		if message == "" {
			message = "upstream request failed"
		}
		return stream, upstreamHTTPError{Status: resp.StatusCode, Message: message}
	}
	return stream, nil
}

// readStream sends parsed deltas in wire order. EOF before [DONE] is failure.
func (c deepSeekClient) readStream(ctx context.Context, body io.Reader, deltas chan<- deepSeekDelta) (upstreamResult, error) {
	var result upstreamResult
	result.Message.Role = "assistant"
	calls := map[int]*toolCall{}
	seenFinish := false
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 16*1024), 2*1024*1024)
	var data []string
	process := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			if result.FinishReason != "stop" && result.FinishReason != "tool_calls" {
				return false, fmt.Errorf("upstream finish_reason=%s", result.FinishReason)
			}
			if (result.FinishReason == "tool_calls") != (len(calls) > 0) {
				return false, errors.New("tool finish mismatch")
			}
			seenIDs := map[string]bool{}
			for index := 0; index < len(calls); index++ {
				call := calls[index]
				if call == nil || call.ID == "" || seenIDs[call.ID] || call.Type != "function" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
					return false, errors.New("invalid tool call")
				}
				seenIDs[call.ID] = true
				result.Message.ToolCalls = append(result.Message.ToolCalls, *call)
			}
			return true, nil
		}
		var frame struct {
			Choices []struct {
				Delta struct {
					Reasoning string `json:"reasoning_content"`
					Answer    string `json:"content"`
					ToolCalls []struct {
						Index    int          `json:"index"`
						ID       string       `json:"id"`
						Type     string       `json:"type"`
						Function functionCall `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			return false, fmt.Errorf("invalid upstream SSE JSON: %w", err)
		}
		result.Chunks++
		if frame.Error != nil {
			return false, errors.New(strings.ReplaceAll(frame.Error.Message, c.key, "[redacted]"))
		}
		if len(frame.Choices) == 0 {
			return false, nil
		} // usage-only frame
		choice := frame.Choices[0]
		if seenFinish && (choice.Delta.Reasoning != "" || choice.Delta.Answer != "" || len(choice.Delta.ToolCalls) > 0) {
			return false, errors.New("upstream delta after finish_reason")
		}
		if choice.FinishReason != nil {
			if seenFinish {
				return false, errors.New("upstream repeated finish_reason")
			}
			seenFinish = true
			result.FinishReason = *choice.FinishReason
		}
		for _, delta := range choice.Delta.ToolCalls {
			if delta.Index < 0 || delta.Index >= 8 {
				return false, errors.New("too many tool calls")
			}
			if calls[delta.Index] == nil {
				calls[delta.Index] = &toolCall{}
			}
			call := calls[delta.Index]
			call.ID += delta.ID
			call.Type += delta.Type
			call.Function.Name += delta.Function.Name
			call.Function.Arguments += delta.Function.Arguments
			if len(call.ID) > 256 || len(call.Type) > 32 || len(call.Function.Name) > 128 || len(call.Function.Arguments) > 16384 {
				return false, errors.New("oversized tool call")
			}
		}
		result.Message.Reasoning += choice.Delta.Reasoning
		result.Message.Content += choice.Delta.Answer
		if choice.Delta.Answer != "" && result.FirstAnswer.IsZero() {
			result.FirstAnswer = time.Now()
		}
		if len(result.Message.Reasoning)+len(result.Message.Content) > 2*1024*1024 {
			return false, errors.New("oversized assistant turn")
		}
		if choice.Delta.Reasoning != "" || choice.Delta.Answer != "" {
			select {
			case deltas <- deepSeekDelta{Reasoning: choice.Delta.Reasoning, Answer: choice.Delta.Answer}:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := process()
			if err != nil || done {
				return result, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if len(data) != 0 {
		done, err := process()
		if err != nil || done {
			return result, err
		}
	}
	return result, errors.New("upstream SSE ended before [DONE]")
}

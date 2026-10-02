package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Control packets travel over the proven reasoningText channel and are removed
// by the r4 card before rendering/hashing. They never enter text.content/TTS.
const controlStart = "\x1eMYAI4:"
const controlEnd = "\x1f"

func (e *youdaoEncoder) searchState(state string, count int) error {
	payload, _ := json.Marshal(map[string]any{"state": state, "count": count, "request_id": e.messageID})
	return e.reasoning(controlStart + string(payload) + controlEnd)
}

// pumpTurn preserves r3's single writer, cancellation and 80ms flush behavior.
func (b *bridge) pumpTurn(ctx context.Context, upstream upstreamStream, session *streamSession) (upstreamResult, error) {
	defer upstream.Body.Close()
	type outcome struct {
		result upstreamResult
		err    error
	}
	deltas := make(chan deepSeekDelta, 16)
	finished := make(chan outcome, 1)
	go func() {
		stats, err := b.client.readStream(ctx, upstream.Body, deltas)
		close(deltas)
		finished <- outcome{stats, err}
	}()
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return upstreamResult{}, ctx.Err()
		case delta, open := <-deltas:
			if !open {
				result := <-finished
				if result.err != nil {
					return result.result, result.err
				}
				return result.result, session.flush()
			}
			if err := session.consume(delta); err != nil {
				return upstreamResult{}, err
			}
		case <-ticker.C:
			if err := session.flush(); err != nil {
				return upstreamResult{}, err
			}
		}
	}
}

func (b *bridge) serveSearchChat(w http.ResponseWriter, r *http.Request) {
	request, err := parseChatRequest(w, r)
	if err != nil {
		http.Error(w, err.Error(), inputStatus(err))
		return
	}
	select {
	case b.busy <- struct{}{}:
		defer func() { <-b.busy }()
	default:
		http.Error(w, "bridge busy", 429)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	encoder, err := newYoudaoEncoder(w, flusher, request)
	if err != nil {
		http.Error(w, "bridge identity failed", 500)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	messages, err := b.contextMessages(request, encoder)
	if err != nil {
		http.Error(w, "当前输入超出上下文预算，请开始新话题或缩短输入", http.StatusRequestEntityTooLarge)
		return
	}
	started := time.Now()
	session := &streamSession{encoder: encoder, stage: stageIdle, started: started, scanDisplay: newScanDisplayAdapter()}
	var sources []SearchSource
	seenSources := map[string]bool{}
	chunks, subturns, searchCalls := 0, 0, 0
	begun := false
	fail := func(stage string) {
		cancel()
		if begun && r.Context().Err() == nil {
			_ = encoder.fail()
		} else if !begun {
			http.Error(w, "upstream request failed", 502)
		}
		log.Printf("[LITTLEP-BRIDGE] stream_interrupted message_id=%s stage=%s", encoder.messageID, stage)
	}
	failUpstream := func(stage string, cause error) {
		failure := classifyUpstreamFailure(cause, request.Scan != nil)
		log.Printf("[LITTLEP-BRIDGE] upstream_failure message_id=%s stage=%s kind=%s upstream_http=%d response_http=%d", encoder.messageID, stage, failure.Kind, failure.HTTP, failure.Status)
		cancel()
		if begun && r.Context().Err() == nil {
			_ = encoder.failWithMessage(failure.Message)
		} else if !begun {
			http.Error(w, failure.Message, failure.Status)
		}
	}
	for round := 0; round <= maxSearchUses; round++ {
		upstream, openErr := b.client.openMessages(ctx, messages, true, round == maxSearchUses || searchCalls >= maxSearchUses)
		if openErr != nil {
			failUpstream("MODEL_OPEN", openErr)
			return
		}
		subturns++
		if !begun {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			if err := encoder.begin(); err != nil {
				upstream.Body.Close()
				fail("CLIENT_WRITE")
				return
			}
			begun = true
			if err := encoder.searchState("init", 0); err != nil {
				upstream.Body.Close()
				fail("CLIENT_WRITE")
				return
			}
		}
		outcome, pumpErr := b.pumpTurn(ctx, upstream, session)
		chunks += outcome.Chunks
		if pumpErr != nil {
			failUpstream("MODEL_STREAM_"+string(session.stage), pumpErr)
			return
		}
		session.finishDisplayTurn()
		if outcome.FinishReason == "stop" {
			if strings.TrimSpace(session.answer) == "" {
				fail("NO_ANSWER")
				return
			}
			// Model answer hash is distinct from the display answer with source footer.
			modelHash := sha256.Sum256([]byte(outcome.Message.Content))
			footer := sourceFooter(sources)
			if footer != "" {
				if err := session.consume(deepSeekDelta{Answer: footer}); err != nil {
					fail("FOOTER")
					return
				}
			}
			session.finishDisplayTurn()
			if err := session.flush(); err != nil {
				fail("CLIENT_WRITE")
				return
			}
			session.stage = stageDone
			if err := encoder.finish(); err != nil {
				fail("CLIENT_WRITE")
				return
			}
			log.Printf("[LITTLEP-BRIDGE] search_summary message_id=%s triggered=%t search_calls=%d sources=%d subturns=%d final_answer_ttft_ms=%d model_answer_chars=%d model_answer_sha256=%x", encoder.messageID, searchCalls > 0, searchCalls, len(sources), subturns, millisecondsSince(started, outcome.FirstAnswer), len([]rune(outcome.Message.Content)), modelHash)
			b.commitContext(ctx, request, encoder, outcome.Message.Content, sources)
			session.completionLog(upstream.HTTPStatus, chunks)
			return
		}
		if round == maxSearchUses || searchCalls >= maxSearchUses {
			fail("UNEXPECTED_TOOL_AFTER_LIMIT")
			return
		}
		// Assistant preambles are legitimate content, not reasoning. Preserve them
		// in the original text channel and replay the exact assistant message. Each
		// subsequent sub-turn starts a fresh reasoning phase; the UI deduplicates
		// native cumulative reasoning cards using this request's transport ID.
		if outcome.Message.Content != "" {
			if err := session.consume(deepSeekDelta{Answer: "\n\n"}); err != nil {
				fail("PREAMBLE_SEPARATOR")
				return
			}
			if err := session.flush(); err != nil {
				fail("CLIENT_WRITE")
				return
			}
		}
		session.stage = stageReasoning
		messages = append(messages, outcome.Message) // full reasoning/content/tool_calls
		for _, call := range outcome.Message.ToolCalls {
			var args struct {
				Query string `json:"query"`
			}
			var result *SearchResult
			toolError := ""
			if call.Function.Name != "web_search" || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || strings.TrimSpace(args.Query) == "" || len(args.Query) > 4096 {
				toolError = "web_search failed: invalid query or unsupported tool; no current information retrieved"
			} else if searchCalls >= maxSearchUses {
				toolError = "web_search failed: search limit reached; no additional current information retrieved"
			} else {
				if err := encoder.searchState("searching", 0); err != nil {
					fail("CLIENT_WRITE")
					return
				}
				searchCalls++
				searchStarted := time.Now()
				result, err = b.search.Search(ctx, args.Query)
				if ctx.Err() != nil {
					fail("SEARCH")
					return
				}
				if err != nil {
					toolError = "web_search failed: current search results could not be retrieved. State this limitation if answering current-information questions."
					if err := encoder.searchState("failed", 0); err != nil {
						fail("CLIENT_WRITE")
						return
					}
				} else {
					for _, source := range result.Sources {
						if !seenSources[source.URL] && len(sources) < 8 {
							sources = append(sources, source)
							seenSources[source.URL] = true
						}
					}
					if err := encoder.searchState("complete", len(result.Sources)); err != nil {
						fail("CLIENT_WRITE")
						return
					}
				}
				domains := []string{}
				if result != nil {
					for _, source := range result.Sources {
						u, _ := url.Parse(source.URL)
						if u != nil {
							domains = append(domains, u.Hostname())
						}
					}
				}

				log.Printf("[LITTLEP-BRIDGE] search message_id=%s latency_ms=%d ok=%t count=%d domains=%v", encoder.messageID, time.Since(searchStarted).Milliseconds(), err == nil, len(domains), domains)
			}
			var toolBody []byte
			if toolError != "" {
				toolBody, _ = json.Marshal(map[string]any{"ok": false, "error": toolError})
			} else {
				toolBody, _ = json.Marshal(map[string]any{"ok": true, "retrieved_at_utc": time.Now().UTC().Format(time.RFC3339), "external_untrusted_reference_data": result})
			}
			messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: string(toolBody)})
		}
	}
	fail("LOOP_LIMIT")
}

func sourceFooter(sources []SearchSource) string {
	if len(sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n来源\n")
	for i, source := range sources {
		if i >= 5 {
			break
		}
		title := strings.Join(strings.Fields(source.Title), " ")
		u, _ := url.Parse(source.URL)
		if u == nil {
			continue
		}
		if title == "" {
			title = u.Hostname()
		}
		runes := []rune(title)
		if len(runes) > 100 {
			title = string(runes[:100]) + "…"
		}
		fmt.Fprintf(&b, "[%d] %s\n%s\n", i+1, title, u.Hostname())
	}
	return b.String()
}

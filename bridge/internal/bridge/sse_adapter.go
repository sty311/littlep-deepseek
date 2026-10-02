package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type streamStage string

const (
	stageIdle      streamStage = "IDLE"
	stageReasoning streamStage = "REASONING"
	stageAnswer    streamStage = "ANSWER"
	stageDone      streamStage = "DONE"
)

type youdaoEncoder struct {
	w         http.ResponseWriter
	flusher   http.Flusher
	meta      map[string]any
	chatID    string
	messageID string
	seeded    bool
}

func newYoudaoEncoder(w http.ResponseWriter, flusher http.Flusher, request chatRequest) (*youdaoEncoder, error) {
	messageID, err := randomID("A_")
	if err != nil {
		return nil, err
	}
	chatID := request.ChatID
	if chatID == "" {
		chatID, err = randomID("")
		if err != nil {
			return nil, err
		}
	}
	return &youdaoEncoder{w: w, flusher: flusher, chatID: chatID, messageID: messageID,
		meta: map[string]any{"messageScene": request.Scene, "messageId": messageID, "messageRole": "assistant"}}, nil
}

func (e *youdaoEncoder) write(event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.w, "event:%s\ndata:%s\n\n", event, data); err != nil {
		return err
	}
	e.flusher.Flush()
	return nil
}

func (e *youdaoEncoder) envelope(items ...any) map[string]any {
	return map[string]any{"msg": "成功", "code": 0, "data": map[string]any{"msg": e.meta, "list": items}}
}

func (e *youdaoEncoder) begin() error {
	return e.write("begin", e.envelope(map[string]any{"type": "chat", "chat": map[string]any{"chatId": e.chatID}}))
}

func (e *youdaoEncoder) reasoning(text string) error {
	if !e.seeded {
		// Preserve the r2 progress seed and native mapping. Reasoning never enters
		// text.content, which the native layer converts to tts_sentence.
		seed := map[string]any{"type": "dayiProgress", "dayiProgress": map[string]any{"type": "reasoningSummary", "title": "思考过程"}}
		if err := e.write("message", e.envelope(seed)); err != nil {
			return err
		}
		e.seeded = true
	}
	item := map[string]any{"type": "text", "text": map[string]any{"type": "reasoningText", "content": "", "content_latex": text}}
	return e.write("message", e.envelope(item))
}

func (e *youdaoEncoder) answer(text string) error {
	item := map[string]any{"type": "text", "text": map[string]any{"type": "text", "content": text}}
	return e.write("message", e.envelope(item))
}

func (e *youdaoEncoder) scanAnswer(raw, display string) error {
	item := map[string]any{"type": "text", "text": map[string]any{"type": "text", "content": raw, "content_latex": display}}
	return e.write("message", e.envelope(item))
}

func (e *youdaoEncoder) finish() error {
	done := map[string]any{"msg": "成功", "code": 0, "data": map[string]any{"newChatStatus": 0, "sse_failure_errorcode": ""}}
	if err := e.write("message", done); err != nil {
		return err
	}
	if _, err := fmt.Fprint(e.w, "event:end\ndata:[DONE]\n\n"); err != nil {
		return err
	}
	e.flusher.Flush()
	return nil
}

func (e *youdaoEncoder) fail() error {
	return e.failWithMessage("生成中断")
}

func (e *youdaoEncoder) failWithMessage(message string) error {
	// Native processFormatData dispatches numeric nonzero code to its error
	// callback. Never send the success completion or event:end after this.
	return e.write("message", map[string]any{"code": 1, "msg": message, "data": map[string]any{"msg": e.meta}})
}

type streamSession struct {
	encoder        *youdaoEncoder
	stage          streamStage
	started        time.Time
	firstToken     time.Time
	firstReason    time.Time
	lastReason     time.Time
	firstAnswer    time.Time
	reasoning      string
	answer         string
	displayAnswer  string
	scanDisplay    *scanDisplayAdapter
	reasonPending  string
	answerPending  string
	displayPending string
	reasonBatches  int
	answerBatches  int
}

func (s *streamSession) consume(delta deepSeekDelta) error {
	now := time.Now()
	if delta.Reasoning != "" {
		if s.stage == stageAnswer || s.stage == stageDone {
			return errors.New("reasoning delta arrived after answer")
		}
		if s.stage == stageIdle {
			s.stage = stageReasoning
		}
		if s.firstToken.IsZero() {
			s.firstToken = now
		}
		if s.firstReason.IsZero() {
			s.firstReason = now
		}
		s.lastReason = now
		s.reasoning += delta.Reasoning
		s.reasonPending += delta.Reasoning
		if len(s.reasoning) > 1024*1024 {
			return errors.New("reasoning exceeded 1 MiB")
		}
	}
	if delta.Answer != "" {
		if s.stage == stageReasoning {
			if err := s.flushReasoning(); err != nil {
				return err
			}
		}
		if s.stage == stageIdle || s.stage == stageReasoning {
			s.stage = stageAnswer
		}
		if s.stage != stageAnswer {
			return errors.New("answer delta after DONE")
		}
		if s.firstToken.IsZero() {
			s.firstToken = now
		}
		if s.firstAnswer.IsZero() {
			s.firstAnswer = now
		}
		s.answer += delta.Answer
		if s.scanDisplay == nil {
			s.answerPending += delta.Answer
		} else {
			raw, display := s.scanDisplay.consume(delta.Answer)
			s.answerPending += raw
			s.displayPending += display
			s.displayAnswer += display
		}
		if len(s.answer) > 1024*1024 {
			return errors.New("answer exceeded 1 MiB")
		}
	}
	return nil
}

func (s *streamSession) flushReasoning() error {
	if s.reasonPending == "" {
		return nil
	}
	batch := s.reasonPending
	if err := s.encoder.reasoning(batch); err != nil {
		return err
	}
	s.reasonPending = ""
	s.reasonBatches++
	hash := sha256.Sum256([]byte(batch))
	log.Printf("[LITTLEP-BRIDGE] batch message_id=%s phase=reasoning seq=%d chars=%d sha256=%x", s.encoder.messageID, s.reasonBatches, len([]rune(batch)), hash)
	return nil
}

func (s *streamSession) flushAnswer() error {
	if s.answerPending == "" {
		return nil
	}
	batch := s.answerPending
	if s.scanDisplay == nil {
		if err := s.encoder.answer(batch); err != nil {
			return err
		}
	} else {
		if s.displayPending == "" {
			return nil
		}
		if err := s.encoder.scanAnswer(batch, s.displayPending); err != nil {
			return err
		}
	}
	s.answerPending = ""
	s.answerBatches++
	if s.scanDisplay == nil {
		hash := sha256.Sum256([]byte(batch))
		log.Printf("[LITTLEP-BRIDGE] batch message_id=%s phase=answer seq=%d chars=%d sha256=%x", s.encoder.messageID, s.answerBatches, len([]rune(batch)), hash)
	} else {
		hash := sha256.Sum256([]byte(s.displayPending))
		rawHash := sha256.Sum256([]byte(batch))
		log.Printf("[LITTLEP-BRIDGE] batch message_id=%s phase=answer seq=%d chars=%d sha256=%x raw_chars=%d raw_sha256=%x", s.encoder.messageID, s.answerBatches, len([]rune(s.displayPending)), hash, len([]rune(batch)), rawHash)
		s.displayPending = ""
	}
	return nil
}

func (s *streamSession) finishDisplayTurn() {
	if s.scanDisplay == nil {
		return
	}
	raw, display := s.scanDisplay.finish()
	s.answerPending += raw
	s.displayPending += display
	s.displayAnswer += display
}

func (s *streamSession) flush() error {
	if err := s.flushReasoning(); err != nil {
		return err
	}
	return s.flushAnswer()
}

func millisecondsSince(started, mark time.Time) int64 {
	if mark.IsZero() {
		return -1
	}
	return mark.Sub(started).Milliseconds()
}

func (s *streamSession) completionLog(httpStatus, chunks int) {
	reasonHash := sha256.Sum256([]byte(s.reasoning))
	answerHash := sha256.Sum256([]byte(s.answer))
	var reasonDuration int64 = -1
	if !s.firstReason.IsZero() {
		reasonDuration = s.lastReason.Sub(s.firstReason).Milliseconds()
	}
	if s.scanDisplay != nil {
		displayHash := sha256.Sum256([]byte(s.displayAnswer))
		log.Printf("[LITTLEP-BRIDGE] completed message_id=%s http=%d ttft_ms=%d reasoning_ttft_ms=%d reasoning_duration_ms=%d answer_ttft_ms=%d total_ms=%d upstream_chunks=%d reasoning_batches=%d answer_batches=%d reasoning_chars=%d reasoning_sha256=%x answer_chars=%d answer_sha256=%x raw_answer_chars=%d raw_answer_sha256=%x",
			s.encoder.messageID, httpStatus, millisecondsSince(s.started, s.firstToken), millisecondsSince(s.started, s.firstReason), reasonDuration,
			millisecondsSince(s.started, s.firstAnswer), time.Since(s.started).Milliseconds(), chunks,
			s.reasonBatches, s.answerBatches, len([]rune(s.reasoning)), reasonHash, len([]rune(s.displayAnswer)), displayHash, len([]rune(s.answer)), answerHash)
		return
	}
	log.Printf("[LITTLEP-BRIDGE] completed message_id=%s http=%d ttft_ms=%d reasoning_ttft_ms=%d reasoning_duration_ms=%d answer_ttft_ms=%d total_ms=%d upstream_chunks=%d reasoning_batches=%d answer_batches=%d reasoning_chars=%d reasoning_sha256=%x answer_chars=%d answer_sha256=%x",
		s.encoder.messageID, httpStatus, millisecondsSince(s.started, s.firstToken), millisecondsSince(s.started, s.firstReason), reasonDuration,
		millisecondsSince(s.started, s.firstAnswer), time.Since(s.started).Milliseconds(), chunks,
		s.reasonBatches, s.answerBatches, len([]rune(s.reasoning)), reasonHash, len([]rune(s.answer)), answerHash)
}

func (b *bridge) serveChat(w http.ResponseWriter, r *http.Request) {
	if b.search != nil {
		b.serveSearchChat(w, r)
		return
	}
	request, err := parseChatRequest(w, r)
	if err != nil {
		log.Printf("[LITTLEP-BRIDGE] rejected method=%s body_bytes=%d reason=%s", r.Method, r.ContentLength, err)
		http.Error(w, err.Error(), inputStatus(err))
		return
	}
	select {
	case b.busy <- struct{}{}:
		defer func() { <-b.busy }()
	default:
		http.Error(w, "bridge busy", http.StatusTooManyRequests)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	encoder, err := newYoudaoEncoder(w, flusher, request)
	if err != nil {
		http.Error(w, "bridge identity failed", http.StatusInternalServerError)
		return
	}
	messageID := encoder.messageID
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	messages, err := b.contextMessages(request, encoder)
	if err != nil {
		http.Error(w, "当前输入超出上下文预算，请开始新话题或缩短输入", http.StatusRequestEntityTooLarge)
		return
	}
	upstream, err := b.client.openMessages(ctx, messages, false, false)
	if err != nil {
		log.Printf("[LITTLEP-BRIDGE] upstream_failed message_id=%s http=%d elapsed_ms=%d", messageID, upstream.HTTPStatus, time.Since(upstream.Started).Milliseconds())
		status, message := upstreamClientError(err, request.Scan != nil)
		http.Error(w, message, status)
		return
	}
	defer upstream.Body.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := encoder.begin(); err != nil {
		cancel()
		log.Printf("[LITTLEP-BRIDGE] client_write_failed message_id=%s", messageID)
		return
	}

	deltas := make(chan deepSeekDelta, 16)
	finished := make(chan struct {
		stats upstreamResult
		err   error
	}, 1)
	go func() {
		stats, streamErr := b.client.readStream(ctx, upstream.Body, deltas)
		close(deltas)
		finished <- struct {
			stats upstreamResult
			err   error
		}{stats, streamErr}
	}()
	session := &streamSession{encoder: encoder, stage: stageIdle, started: upstream.Started, scanDisplay: newScanDisplayAdapter()}
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				_ = encoder.fail()
			}
			log.Printf("[LITTLEP-BRIDGE] stream_interrupted message_id=%s stage=%s", messageID, session.stage)
			return
		case delta, open := <-deltas:
			if !open {
				outcome := <-finished
				if outcome.err != nil {
					_ = encoder.fail()
					log.Printf("[LITTLEP-BRIDGE] stream_failed message_id=%s stage=%s upstream_chunks=%d", messageID, session.stage, outcome.stats.Chunks)
					return
				}
				if strings.TrimSpace(session.answer) == "" {
					_ = encoder.fail()
					log.Printf("[LITTLEP-BRIDGE] stream_failed message_id=%s reason=no_answer upstream_chunks=%d", messageID, outcome.stats.Chunks)
					return
				}
				session.finishDisplayTurn()
				if err := session.flush(); err != nil {
					cancel()
					log.Printf("[LITTLEP-BRIDGE] client_write_failed message_id=%s", messageID)
					return
				}
				session.stage = stageDone
				if err := encoder.finish(); err != nil {
					cancel()
					log.Printf("[LITTLEP-BRIDGE] client_write_failed message_id=%s", messageID)
					return
				}
				b.commitContext(ctx, request, encoder, outcome.stats.Message.Content, nil)
				session.completionLog(upstream.HTTPStatus, outcome.stats.Chunks)
				return
			}
			if err := session.consume(delta); err != nil {
				cancel()
				_ = encoder.fail()
				log.Printf("[LITTLEP-BRIDGE] stream_failed message_id=%s reason=invalid_delta stage=%s", messageID, session.stage)
				return
			}
		case <-ticker.C:
			if err := session.flush(); err != nil {
				cancel()
				log.Printf("[LITTLEP-BRIDGE] client_write_failed message_id=%s", messageID)
				return
			}
		}
	}
}

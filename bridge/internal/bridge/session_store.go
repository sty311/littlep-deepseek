package bridge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	sessionTTL             = 2 * time.Hour
	maxSessions            = 8
	maxStoredImageBytes    = 64 << 20
	maxSessionTextBytes    = 4 << 20
	maxStoredPairTextBytes = 2 << 20
	maxSessionPairs        = 64
	maxStoredSources       = 8
)

// SessionPair holds only a completed user/assistant exchange. Scan is the
// original JPEG, retained for the latest successful scan in a session only.
type SessionPair struct {
	Question string
	Answer   string
	Scan     *ScanInput
}

// SessionSnapshot owns all of its slices. Mutating it cannot alter the store.
type SessionSnapshot struct {
	Pairs       []SessionPair
	Sources     []SearchSource
	SourceFocus int // 1..8 means the last explicitly discussed source
}

type storedSession struct {
	pairs       []SessionPair
	sources     []SearchSource
	sourceFocus int
	updatedAt   time.Time
}

// SessionStore is a small, bounded, in-memory cache of successful turns.
// Calls from concurrent HTTP requests are serialized by mu.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*storedSession
	now      func() time.Time
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]*storedSession), now: time.Now}
}

func cloneScan(scan *ScanInput) *ScanInput {
	if scan == nil {
		return nil
	}
	copy := *scan
	copy.Bytes = append([]byte(nil), scan.Bytes...)
	return &copy
}

func clonePair(pair SessionPair) SessionPair {
	pair.Scan = cloneScan(pair.Scan)
	return pair
}

func cloneSources(sources []SearchSource) []SearchSource {
	return append([]SearchSource(nil), sources...)
}

func (s *SessionStore) expireLocked(now time.Time) {
	for id, session := range s.sessions {
		if !now.Before(session.updatedAt.Add(sessionTTL)) {
			delete(s.sessions, id)
		}
	}
}

func (s *SessionStore) Snapshot(sessionID string) SessionSnapshot {
	if s == nil || sessionID == "" {
		return SessionSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.now())
	session := s.sessions[sessionID]
	if session == nil {
		return SessionSnapshot{}
	}
	snapshot := SessionSnapshot{Sources: cloneSources(session.sources), SourceFocus: session.sourceFocus}
	snapshot.Pairs = make([]SessionPair, len(session.pairs))
	for i, pair := range session.pairs {
		snapshot.Pairs[i] = clonePair(pair)
	}
	return snapshot
}

func boundSourceText(value string, limit int) string {
	value = strings.ReplaceAll(value, controlStart, "")
	value = strings.ReplaceAll(value, controlEnd, "")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	// Keep valid UTF-8 while bounding retained data.
	for limit > 0 && !utf8Boundary(value, limit) {
		limit--
	}
	return value[:limit]
}

// DebugSessionSummary intentionally omits questions, answers, image bytes,
// source details, and upstream credentials.
type DebugSessionSummary struct {
	SessionID   string    `json:"session_id"`
	Turns       int       `json:"turns"`
	UpdatedAt   time.Time `json:"updated_at"`
	ImageCount  int       `json:"image_count"`
	SourceCount int       `json:"source_count"`
}

func (s *SessionStore) DebugSessions() []DebugSessionSummary {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.now())
	result := make([]DebugSessionSummary, 0, len(s.sessions))
	for id, session := range s.sessions {
		summary := DebugSessionSummary{SessionID: id, Turns: len(session.pairs), UpdatedAt: session.updatedAt, SourceCount: len(session.sources)}
		for _, pair := range session.pairs {
			if pair.Scan != nil {
				summary.ImageCount++
			}
		}
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].SessionID < result[j].SessionID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func utf8Boundary(value string, index int) bool {
	return index == len(value) || index == 0 || value[index]&0xc0 != 0x80
}

func boundedSources(sources []SearchSource) []SearchSource {
	if len(sources) > maxStoredSources {
		sources = sources[:maxStoredSources]
	}
	result := make([]SearchSource, 0, len(sources))
	for _, source := range sources {
		result = append(result, SearchSource{
			URL:     boundSourceText(source.URL, 2048),
			Title:   boundSourceText(source.Title, 256),
			Snippet: boundSourceText(source.Snippet, 1024),
			PageAge: boundSourceText(source.PageAge, 64),
		})
	}
	return result
}

func sessionTextBytes(pairs []SessionPair) int {
	total := 0
	for _, pair := range pairs {
		total += len(pair.Question) + len(pair.Answer)
	}
	return total
}

func (s *SessionStore) enforceImageLimitLocked(preferredID string) {
	for {
		used := 0
		oldestID := ""
		var oldestTime time.Time
		for id, session := range s.sessions {
			for _, pair := range session.pairs {
				if pair.Scan != nil {
					used += len(pair.Scan.Bytes)
					if id != preferredID && (oldestID == "" || session.updatedAt.Before(oldestTime)) {
						oldestID, oldestTime = id, session.updatedAt
					}
				}
			}
		}
		if used <= maxStoredImageBytes || oldestID == "" {
			return
		}
		delete(s.sessions, oldestID)
	}
}

func (s *SessionStore) evictOldestSessionLocked(exceptID string) {
	oldestID := ""
	var oldestTime time.Time
	for id, session := range s.sessions {
		if id != exceptID && (oldestID == "" || session.updatedAt.Before(oldestTime)) {
			oldestID, oldestTime = id, session.updatedAt
		}
	}
	if oldestID != "" {
		delete(s.sessions, oldestID)
	}
}

// Commit must be called only after the final SSE completion write succeeds.
// A canceled request leaves all prior state unchanged. Empty sources preserve
// the most recent successful search metadata for follow-up questions.
func (s *SessionStore) Commit(ctx context.Context, sessionID string, input Input, finalAnswer string, sources []SearchSource) error {
	if s == nil || ctx == nil || strings.TrimSpace(sessionID) == "" {
		return errors.New("session store, context, or session ID is missing")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(input.Question) == "" || strings.TrimSpace(finalAnswer) == "" {
		return errors.New("cannot store an incomplete turn")
	}
	if len(input.Question)+len(finalAnswer) > maxStoredPairTextBytes {
		return errors.New("completed turn exceeds pair text limit")
	}
	if input.Scan != nil && len(input.Scan.Bytes) > maxImageBytes {
		return errors.New("scan exceeds image size limit")
	}
	if strings.Contains(finalAnswer, controlStart) {
		return errors.New("final answer contains a control packet")
	}

	// Clone before acquiring the lock, then repeat cancellation check inside it.
	pair := SessionPair{Question: input.Question, Answer: finalAnswer, Scan: cloneScan(input.Scan)}
	newSources := boundedSources(sources)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	s.expireLocked(now)
	session := s.sessions[sessionID]
	if session == nil {
		if len(s.sessions) >= maxSessions {
			s.evictOldestSessionLocked("")
		}
		session = &storedSession{}
		s.sessions[sessionID] = session
	}
	if pair.Scan != nil {
		for i := range session.pairs {
			session.pairs[i].Scan = nil
		}
	}
	session.pairs = append(session.pairs, pair)
	if len(newSources) > 0 {
		session.sources = newSources
		session.sourceFocus = 0
	} else if sourceIndex := referencedSourceIndex(input.Question); sourceIndex >= 0 && sourceIndex < len(session.sources) {
		session.sourceFocus = sourceIndex + 1
	} else if asksAboutSources(input.Question) {
		session.sourceFocus = 0
	}
	for len(session.pairs) > maxSessionPairs || sessionTextBytes(session.pairs) > maxSessionTextBytes {
		oldestUnpinned := -1
		for i := 0; i < len(session.pairs)-1; i++ {
			if session.pairs[i].Scan == nil {
				oldestUnpinned = i
				break
			}
		}
		if oldestUnpinned < 0 {
			break // latest image pair and newest pair are each <= 2 MiB
		}
		copy(session.pairs[oldestUnpinned:], session.pairs[oldestUnpinned+1:])
		session.pairs[len(session.pairs)-1] = SessionPair{}
		session.pairs = session.pairs[:len(session.pairs)-1]
	}
	session.updatedAt = now
	s.enforceImageLimitLocked(sessionID)
	return nil
}

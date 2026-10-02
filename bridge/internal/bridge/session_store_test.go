package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func commitTurn(t *testing.T, store *SessionStore, sessionID, question, answer string, scan *ScanInput, sources []SearchSource) {
	t.Helper()
	if err := store.Commit(context.Background(), sessionID, Input{Question: question, Scan: scan}, answer, sources); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStoreTwoRoundsIsolationAndClone(t *testing.T) {
	store := NewSessionStore()
	commitTurn(t, store, "A", "first", "answer one", nil, nil)
	commitTurn(t, store, "A", "second", "answer two", nil, nil)
	commitTurn(t, store, "B", "other", "other answer", nil, nil)
	snapshot := store.Snapshot("A")
	if len(snapshot.Pairs) != 2 || snapshot.Pairs[0].Question != "first" || snapshot.Pairs[1].Answer != "answer two" {
		t.Fatalf("bad A snapshot: %+v", snapshot)
	}
	if got := store.Snapshot("B"); len(got.Pairs) != 1 || got.Pairs[0].Question != "other" {
		t.Fatalf("sessions crossed: %+v", got)
	}
	snapshot.Pairs[0].Answer = "tampered"
	if store.Snapshot("A").Pairs[0].Answer != "answer one" {
		t.Fatal("snapshot mutation reached store")
	}
}

func TestSessionStoreOriginalImageReplacementAndClone(t *testing.T) {
	store := NewSessionStore()
	firstBytes := []byte("original-jpeg-one")
	first := &ScanInput{Bytes: firstBytes, MIME: "image/jpeg", SHA256: sha256.Sum256(firstBytes), Width: 100, Height: 100}
	commitTurn(t, store, "scan", "what is this", "first image", first, nil)
	firstBytes[0] = 'X'
	snapshot := store.Snapshot("scan")
	if !bytes.Equal(snapshot.Pairs[0].Scan.Bytes, []byte("original-jpeg-one")) || snapshot.Pairs[0].Scan.SHA256 != sha256.Sum256([]byte("original-jpeg-one")) {
		t.Fatal("stored original image or SHA changed")
	}
	snapshot.Pairs[0].Scan.Bytes[0] = 'Y'
	if store.Snapshot("scan").Pairs[0].Scan.Bytes[0] != 'o' {
		t.Fatal("snapshot exposed stored image bytes")
	}
	newBytes := []byte("original-jpeg-two")
	newScan := &ScanInput{Bytes: newBytes, MIME: "image/jpeg", SHA256: sha256.Sum256(newBytes)}
	commitTurn(t, store, "scan", "new scan", "second image", newScan, nil)
	after := store.Snapshot("scan")
	if len(after.Pairs) != 2 || after.Pairs[0].Scan != nil || after.Pairs[1].Scan == nil || after.Pairs[1].Scan.SHA256 != newScan.SHA256 {
		t.Fatalf("new image did not replace old: %+v", after.Pairs)
	}
}

func TestSessionStoreCanceledNewScanKeepsPreviousImage(t *testing.T) {
	store := NewSessionStore()
	old := []byte("old-original")
	commitTurn(t, store, "scan", "old", "old answer", &ScanInput{Bytes: old, MIME: "image/jpeg", SHA256: sha256.Sum256(old)}, nil)
	newImage := []byte("new-uncommitted")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Commit(ctx, "scan", Input{Question: "new", Scan: &ScanInput{Bytes: newImage, MIME: "image/jpeg", SHA256: sha256.Sum256(newImage)}}, "new answer", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled scan, got %v", err)
	}
	snapshot := store.Snapshot("scan")
	if len(snapshot.Pairs) != 1 || snapshot.Pairs[0].Scan == nil || snapshot.Pairs[0].Scan.SHA256 != sha256.Sum256(old) {
		t.Fatal("canceled scan changed retained original image")
	}
}

func TestSessionStoreSourcesAndCanceledPending(t *testing.T) {
	store := NewSessionStore()
	sources := make([]SearchSource, 9)
	for i := range sources {
		sources[i] = SearchSource{URL: fmt.Sprintf("https://example.com/%d", i+1), Title: fmt.Sprintf("source %d", i+1), Snippet: strings.Repeat("untrusted snippet ", 100)}
	}
	commitTurn(t, store, "s", "search", "found sources", nil, sources)
	first := store.Snapshot("s")
	if len(first.Sources) != 8 || first.Sources[1].Title != "source 2" || len(first.Sources[1].Snippet) != 1024 {
		t.Fatalf("sources not bounded metadata: %+v", first.Sources)
	}
	first.Sources[1].Title = "changed"
	if store.Snapshot("s").Sources[1].Title != "source 2" {
		t.Fatal("sources snapshot shared with store")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pendingImage := []byte("pending-original-image")
	if err := store.Commit(ctx, "s", Input{Question: "pending", Scan: &ScanInput{Bytes: pendingImage, MIME: "image/jpeg", SHA256: sha256.Sum256(pendingImage)}}, "never save", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if got := store.Snapshot("s"); len(got.Pairs) != 1 {
		t.Fatalf("canceled pending turn was committed: %+v", got.Pairs)
	}
	commitTurn(t, store, "s", "follow-up", "still here", nil, nil)
	if got := store.Snapshot("s"); len(got.Sources) != 8 || got.Sources[1].Title != "source 2" {
		t.Fatal("empty source result erased latest source metadata")
	}
}

func TestSessionStoreTTLSessionAndImageBounds(t *testing.T) {
	store := NewSessionStore()
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	for i := 0; i < maxSessions+1; i++ {
		commitTurn(t, store, fmt.Sprint(i), "q", "a", nil, nil)
		clock = clock.Add(time.Second)
	}
	if len(store.sessions) != maxSessions || len(store.Snapshot("0").Pairs) != 0 {
		t.Fatalf("session limit not enforced: count=%d", len(store.sessions))
	}
	clock = clock.Add(sessionTTL)
	if got := store.Snapshot("8"); len(got.Pairs) != 0 || len(store.sessions) != 0 {
		t.Fatal("TTL did not expire sessions")
	}

	// Three 22 MiB images exceed 64 MiB. The oldest image session is
	// evicted in full so its text cannot suggest that its image remains.
	for i := 0; i < 3; i++ {
		payload := bytes.Repeat([]byte{byte(i + 1)}, 22<<20)
		commitTurn(t, store, fmt.Sprint(i), "scan", "answer", &ScanInput{Bytes: payload, MIME: "image/jpeg", SHA256: sha256.Sum256(payload)}, nil)
		clock = clock.Add(time.Second)
	}
	used := 0
	for _, session := range store.sessions {
		for _, pair := range session.pairs {
			if pair.Scan != nil {
				used += len(pair.Scan.Bytes)
			}
		}
	}
	if used > maxStoredImageBytes || len(store.Snapshot("0").Pairs) != 0 {
		t.Fatalf("global image bound failed: bytes=%d", used)
	}
}

func TestSessionTextTrimKeepsLatestImagePair(t *testing.T) {
	store := NewSessionStore()
	large := strings.Repeat("a", 600<<10)
	commitTurn(t, store, "s", "old text", large, nil, nil)
	image := []byte("original")
	commitTurn(t, store, "s", "image", large, &ScanInput{Bytes: image, MIME: "image/jpeg", SHA256: sha256.Sum256(image)}, nil)
	for i := 0; i < 8; i++ {
		commitTurn(t, store, "s", fmt.Sprintf("new%d", i), large, nil, nil)
	}
	snapshot := store.Snapshot("s")
	if len(snapshot.Pairs) > maxSessionPairs || sessionTextBytes(snapshot.Pairs) > maxSessionTextBytes {
		t.Fatalf("text not bounded: %d pairs, %d bytes", len(snapshot.Pairs), sessionTextBytes(snapshot.Pairs))
	}
	if snapshot.Pairs[0].Question != "image" || snapshot.Pairs[0].Scan == nil || snapshot.Pairs[len(snapshot.Pairs)-1].Question != "new7" {
		t.Fatalf("latest image pair or newest turn lost: %d pairs", len(snapshot.Pairs))
	}
}

func TestSessionStoreConcurrentCommitsStayComplete(t *testing.T) {
	store := NewSessionStore()
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_ = store.Commit(context.Background(), "same", Input{Question: fmt.Sprintf("q%d", i)}, fmt.Sprintf("a%d", i), nil)
			_ = store.Snapshot("same")
		}(i)
	}
	wait.Wait()
	snapshot := store.Snapshot("same")
	if len(snapshot.Pairs) != 20 {
		t.Fatalf("lost a concurrent complete turn: %d", len(snapshot.Pairs))
	}
	for _, pair := range snapshot.Pairs {
		if pair.Question == "" || pair.Answer == "" {
			t.Fatal("partial pair in snapshot")
		}
	}
}

func TestDebugSessionsContainsSummaryOnly(t *testing.T) {
	store := NewSessionStore()
	commitTurn(t, store, "chat", "private question", "private answer", nil, []SearchSource{{Title: "private title", URL: "https://example.com"}})
	summaries := store.DebugSessions()
	if len(summaries) != 1 || summaries[0].SessionID != "chat" || summaries[0].Turns != 1 || summaries[0].SourceCount != 1 {
		t.Fatalf("incorrect debug summary: %+v", summaries)
	}
	data, err := json.Marshal(summaries)
	if err != nil || strings.Contains(string(data), "private question") || strings.Contains(string(data), "private answer") || strings.Contains(string(data), "private title") {
		t.Fatalf("debug endpoint would leak content: %s, %v", data, err)
	}
}

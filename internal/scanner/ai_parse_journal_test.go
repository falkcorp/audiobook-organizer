// file: internal/scanner/ai_parse_journal_test.go
// version: 1.0.0
// guid: b62c07fa-91e5-4d38-8c74-3a19de5b20f7
// last-edited: 2026-09-20

package scanner

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// parseKV is an in-memory resultjournal.KVStore. It stands in for the pebble
// store; the point of these tests is what SURVIVES it, so the same instance is
// reused across "restarts".
type parseKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newParseKV() *parseKV { return &parseKV{m: map[string][]byte{}} }

func (s *parseKV) SetRaw(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	s.m[key] = cp
	return nil
}

func (s *parseKV) GetRaw(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (s *parseKV) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.m))
	for k := range s.m {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]database.KVPair, 0, len(keys))
	last := ""
	for _, k := range keys {
		out = append(out, database.KVPair{Key: k, Value: s.m[k]})
		last = k
	}
	return out, last, nil
}

func (s *parseKV) DeleteRawBatch(keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.m, k)
	}
	return nil
}

// journalCountingParser records every filename it is asked about.
type journalCountingParser struct {
	mu    sync.Mutex
	asked [][]string
	calls int
	err   error
	// short makes it return one fewer result than asked, to exercise the
	// alignment guard.
	short bool
	// nilFor names filenames the model has nothing to say about.
	nilFor map[string]bool
}

func (p *journalCountingParser) ParseBatch(_ context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	cp := append([]string(nil), filenames...)
	p.asked = append(p.asked, cp)
	if p.err != nil {
		return nil, p.err
	}
	n := len(filenames)
	if p.short && n > 0 {
		n--
	}
	out := make([]*ai.ParsedMetadata, n)
	for i := 0; i < n; i++ {
		if p.nilFor[filenames[i]] {
			continue // leave nil: the model had nothing for this one
		}
		out[i] = &ai.ParsedMetadata{Title: "T:" + filenames[i], Author: "A", Confidence: "high"}
	}
	return out, nil
}

func (p *journalCountingParser) totalAsked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var all []string
	for _, batch := range p.asked {
		all = append(all, batch...)
	}
	return all
}

func newTestJournalledParser(t *testing.T, inner aiBatchParser, kv *parseKV) *journalledParser {
	t.Helper()
	j, err := resultjournal.New(kv, aiParseJournalKind)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	return &journalledParser{
		inner: inner, journal: j, log: logger.New("test"),
		endpoint: "test", model: "test-model",
	}
}

// THE item-5 test: a restart mid-op must not throw away finished LLM work.
// library.ai-parse is ResumeDrop, so the op genuinely re-runs from scratch —
// what must not happen is the MODEL being asked again.
func TestJournalledParser_RestartAsksTheModelNothingTwice(t *testing.T) {
	kv := newParseKV()
	names := []string{"a.mp3", "b.mp3", "c.mp3"}

	first := &journalCountingParser{}
	got1, err := newTestJournalledParser(t, first, kv).ParseBatch(context.Background(), names)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}

	// "Restart": a brand-new parser and decorator over the same durable store.
	second := &journalCountingParser{}
	got2, err := newTestJournalledParser(t, second, kv).ParseBatch(context.Background(), names)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}

	if second.calls != 0 {
		t.Fatalf("after a restart the model was called %d time(s); want 0 (asked: %v)",
			second.calls, second.totalAsked())
	}
	if len(got1) != len(got2) {
		t.Fatalf("result count changed across the restart: %d then %d", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i] == nil || got2[i] == nil || got1[i].Title != got2[i].Title {
			t.Fatalf("result %d differs across the restart: %+v vs %+v", i, got1[i], got2[i])
		}
	}
}

// Only the misses are sent, and the caller's order is preserved.
func TestJournalledParser_SendsOnlyMissesAndKeepsOrder(t *testing.T) {
	kv := newParseKV()
	warm := &journalCountingParser{}
	if _, err := newTestJournalledParser(t, warm, kv).ParseBatch(context.Background(), []string{"b.mp3"}); err != nil {
		t.Fatalf("warm: %v", err)
	}

	p := &journalCountingParser{}
	names := []string{"a.mp3", "b.mp3", "c.mp3"}
	got, err := newTestJournalledParser(t, p, kv).ParseBatch(context.Background(), names)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	asked := p.totalAsked()
	if len(asked) != 2 || asked[0] != "a.mp3" || asked[1] != "c.mp3" {
		t.Fatalf("model asked %v; want only the two misses in order", asked)
	}
	for i, name := range names {
		if got[i] == nil {
			t.Fatalf("position %d (%s) is nil", i, name)
		}
		if got[i].Title != "T:"+name {
			t.Fatalf("position %d holds %q; results are misaligned", i, got[i].Title)
		}
	}
}

// A failed batch journals nothing, so the next run re-asks rather than caching
// an error as if it were an answer.
func TestJournalledParser_FailedBatchJournalsNothing(t *testing.T) {
	kv := newParseKV()
	boom := &journalCountingParser{err: errors.New("model exploded")}
	if _, err := newTestJournalledParser(t, boom, kv).ParseBatch(context.Background(), []string{"a.mp3"}); err == nil {
		t.Fatal("expected the parser error to propagate")
	}

	next := &journalCountingParser{}
	if _, err := newTestJournalledParser(t, next, kv).ParseBatch(context.Background(), []string{"a.mp3"}); err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if next.calls != 1 {
		t.Fatalf("model called %d time(s); a failed batch must not have been cached", next.calls)
	}
}

// The alignment guard. A short reply cannot be matched to its inputs with
// certainty, and writing a positional guess into a CONTENT-keyed journal would
// make one book's metadata permanently answer for another's filename.
func TestJournalledParser_ShortReplyJournalsNothing(t *testing.T) {
	kv := newParseKV()
	short := &journalCountingParser{short: true}
	names := []string{"a.mp3", "b.mp3"}
	if _, err := newTestJournalledParser(t, short, kv).ParseBatch(context.Background(), names); err != nil {
		t.Fatalf("parse: %v", err)
	}

	next := &journalCountingParser{}
	if _, err := newTestJournalledParser(t, next, kv).ParseBatch(context.Background(), names); err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if got := len(next.totalAsked()); got != 2 {
		t.Fatalf("model asked about %d filenames; want both re-asked, nothing cached from a short reply", got)
	}
}

// Bumping the prompt version must invalidate every entry, or a prompt change
// would go on being answered from the old prompt's results forever.
func TestAIParseContentKey_PromptVersionIsInTheKey(t *testing.T) {
	a := resultjournal.ContentKey("x.mp3", aiParsePromptVersion)
	b := resultjournal.ContentKey("x.mp3", aiParsePromptVersion+"-next")
	if a == b {
		t.Fatal("the prompt version is not part of the key; a prompt change could not invalidate anything")
	}
	if a != aiParseContentKey("x.mp3") {
		t.Fatal("aiParseContentKey does not hash (filename, promptVersion)")
	}
}

// Two books whose basenames are identical are sent identical input, so sharing
// one entry is correct rather than an approximation.
func TestAIParseContentKey_SharesOnIdenticalBasenames(t *testing.T) {
	if aiParseContentKey("01.mp3") != aiParseContentKey("01.mp3") {
		t.Fatal("identical inputs must share a key")
	}
	if aiParseContentKey("01.mp3") == aiParseContentKey("02.mp3") {
		t.Fatal("different inputs must not share a key")
	}
}

// The wrap must be live for the parser prod ACTUALLY runs: ai_endpoints
// routing on, mode=local. newAIParser has several returns, and wrapping any one
// branch would leave production untouched while the suite went green.
func TestWithParseJournal_WrapsTheRoutedParserProdUses(t *testing.T) {
	setLLMMode(t, config.AIBackendModeLocal, true)

	inner, ok := newAIParserUnjournalled(logger.New("test"))
	if !ok {
		t.Fatal("parser not enabled")
	}
	if _, routed := inner.(*ai.RoutedFilenameParser); !routed {
		t.Fatalf("fixture is not prod's parser: got %T", inner)
	}

	wrapped := withParseJournal(inner, newParseKV(), logger.New("test"))
	jp, isJournalled := wrapped.(*journalledParser)
	if !isJournalled {
		t.Fatalf("the routed parser was NOT journalled: got %T", wrapped)
	}
	if jp.inner != inner {
		t.Fatal("the journalled parser does not wrap the parser it was given")
	}
}

// database.Store embeds RawKVStore, so production always satisfies KVStore.
// A store that does not must degrade to today's behaviour, not panic or fail
// the parse -- durability is a cache, never a prerequisite.
func TestWithParseJournal_StoreWithoutRawKVDegrades(t *testing.T) {
	inner := &journalCountingParser{}
	type notAKV struct{}
	wrapped := withParseJournal(inner, notAKV{}, logger.New("test"))
	if wrapped != aiBatchParser(inner) {
		t.Fatalf("expected the parser to be returned unchanged, got %T", wrapped)
	}
}

// A filename the model has NOTHING to say about must be journalled too.
// Otherwise the least useful filenames are the ones re-sent to the model on
// every run, forever — the exact waste this decorator exists to remove.
//
// This is the path the other tests never reach: journalCountingParser returns a
// result for every input unless told otherwise, so without this fixture a nil
// mishandling would sit behind a fully green suite.
func TestJournalledParser_NilResultIsJournalledAndStaysNil(t *testing.T) {
	kv := newParseKV()
	names := []string{"known.mp3", "mystery.mp3"}

	first := &journalCountingParser{nilFor: map[string]bool{"mystery.mp3": true}}
	got1, err := newTestJournalledParser(t, first, kv).ParseBatch(context.Background(), names)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	if got1[1] != nil {
		t.Fatalf("fixture did not produce a nil result: %+v", got1[1])
	}

	second := &journalCountingParser{nilFor: map[string]bool{"mystery.mp3": true}}
	got2, err := newTestJournalledParser(t, second, kv).ParseBatch(context.Background(), names)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if second.calls != 0 {
		t.Fatalf("the model was re-asked %v after a restart; a nil result must be journalled too",
			second.totalAsked())
	}
	// A journalled null must come back as a NIL result, never a zero-valued
	// struct: that would turn "no answer" into an answer of empty strings.
	if got2[1] != nil {
		t.Fatalf("journalled nil came back as %+v; want nil", got2[1])
	}
	if got2[0] == nil || got2[0].Title != "T:known.mp3" {
		t.Fatalf("the non-nil neighbour was lost: %+v", got2[0])
	}
}

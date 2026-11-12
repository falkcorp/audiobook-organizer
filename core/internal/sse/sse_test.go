// file: core/internal/sse/sse_test.go
// version: 1.1.0
// guid: 7c1e4b93-5d28-4a6f-9b07-e2a3c8d41f56
// last-edited: 2026-11-12

package sse_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/sse"
)

const nonce = "00000000deadbeef"

// recorder is the ResponseWriter a stream writes to: it keeps every byte,
// records every write deadline, and can be told to fail the next write.
type recorder struct {
	mu        sync.Mutex
	hdr       http.Header
	status    int
	body      strings.Builder
	deadlines []time.Time
	flushes   int
	failWith  error
	noDeadl   bool // SetWriteDeadline unsupported
}

func newRecorder() *recorder { return &recorder{hdr: http.Header{}} }

func (r *recorder) Header() http.Header { return r.hdr }
func (r *recorder) WriteHeader(c int)   { r.mu.Lock(); r.status = c; r.mu.Unlock() }
func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return 0, r.failWith
	}
	return r.body.Write(p)
}
func (r *recorder) FlushError() error { r.mu.Lock(); r.flushes++; r.mu.Unlock(); return nil }
func (r *recorder) SetWriteDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noDeadl {
		return http.ErrNotSupported
	}
	r.deadlines = append(r.deadlines, t)
	return nil
}

func (r *recorder) text() string { r.mu.Lock(); defer r.mu.Unlock(); return r.body.String() }
func (r *recorder) failNext(err error) {
	r.mu.Lock()
	r.failWith = err
	r.mu.Unlock()
}

// awaitText waits until the body holds sub.
func (r *recorder) awaitText(t *testing.T, sub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(r.text(), sub) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("never saw %q in:\n%s", sub, r.text())
}

// judge delivers `{"n":<seq>}` for every entry unless refuse says otherwise.
type judge struct {
	refuse  func(*sse.Entry) bool
	data    func(*sse.Entry) []byte
	close   atomic.Value // string: Check's answer
	checks  atomic.Int64
	beats   atomic.Int64
	deliver atomic.Int64
}

func (j *judge) Deliver(_ context.Context, e *sse.Entry) ([]byte, bool) {
	j.deliver.Add(1)
	if j.refuse != nil && j.refuse(e) {
		return nil, false
	}
	if j.data != nil {
		return j.data(e), true
	}
	return []byte(fmt.Sprintf(`{"n":%d}`, e.Seq)), true
}

func (j *judge) Check(_ context.Context, beat bool) string {
	j.checks.Add(1)
	if beat {
		j.beats.Add(1)
	}
	s, _ := j.close.Load().(string)
	return s
}

func opts() sse.Options {
	return sse.Options{
		RingSize: 8, ClientBuffer: 4, MaxClients: 4, MaxPerBucket: 2,
		Heartbeat: time.Hour, WriteTimeout: 2 * time.Second, Nonce: nonce,
	}
}

func broker(t *testing.T, mod func(*sse.Options)) *sse.Broker {
	t.Helper()
	o := opts()
	if mod != nil {
		mod(&o)
	}
	b, err := sse.NewBroker(o)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pub(b *sse.Broker, topic, key string) uint64 {
	return b.Publish(sse.Entry{Topic: topic, Key: key, Item: topic})
}

func sub(t *testing.T, b *sse.Broker, mod func(*sse.Subscription)) (*sse.Subscriber, *judge) {
	t.Helper()
	j := &judge{}
	req := sse.Subscription{Topics: []string{"a.x", "a.y"}, Judge: j}
	if mod != nil {
		mod(&req)
	}
	s, err := b.Subscribe(req)
	if err != nil {
		t.Fatal(err)
	}
	return s, j
}

// run serves s on a goroutine and returns the recorder and a result channel.
func run(ctx context.Context, s *sse.Subscriber) (*recorder, <-chan string) {
	rec := newRecorder()
	done := make(chan string, 1)
	go func() { done <- s.Serve(ctx, rec) }()
	return rec, done
}

func finished(t *testing.T, done <-chan string) string {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
		return ""
	}
}

func ids(text string) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		if v, ok := strings.CutPrefix(l, "id: "); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestNewBroker_RefusesNonPositiveBounds(t *testing.T) {
	for name, mod := range map[string]func(*sse.Options){
		"RingSize":     func(o *sse.Options) { o.RingSize = 0 },
		"ClientBuffer": func(o *sse.Options) { o.ClientBuffer = -1 },
		"MaxClients":   func(o *sse.Options) { o.MaxClients = 0 },
		"MaxPerBucket": func(o *sse.Options) { o.MaxPerBucket = 0 },
		"Heartbeat":    func(o *sse.Options) { o.Heartbeat = 0 },
		"WriteTimeout": func(o *sse.Options) { o.WriteTimeout = 0 },
	} {
		o := opts()
		mod(&o)
		if _, err := sse.NewBroker(o); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBroker_NonceIsRandomWhenUnset(t *testing.T) {
	o := opts()
	o.Nonce = ""
	a, _ := sse.NewBroker(o)
	b, _ := sse.NewBroker(o)
	if len(a.Nonce()) != 16 || a.Nonce() == b.Nonce() {
		t.Errorf("nonces %q %q", a.Nonce(), b.Nonce())
	}
}

func TestSubscribe_ResumeBoundaries(t *testing.T) {
	b := broker(t, func(o *sse.Options) { o.RingSize = 4; o.ClientBuffer = 8 })
	for range 6 { // seq 1..6; the ring holds 3..6
		pub(b, "a.x", "")
	}
	for _, c := range []struct {
		id        string
		wantReset string
		wantSeqs  []int
	}{
		{nonce + "-2", "", []int{3, 4, 5, 6}}, // last+1 == oldest: nothing missed
		{nonce + "-3", "", []int{4, 5, 6}},
		{nonce + "-5", "", []int{6}},
		{nonce + "-6", "", nil},                          // caught up
		{nonce + "-7", "gap", nil},                       // from the future
		{nonce + "-1", "gap", nil},                       // last+1 < oldest: seq 2 is gone
		{nonce + "-0", "gap", nil},                       //
		{"otherboot-3", "restart", nil},                  // another process
		{strings.Repeat("x", 50) + "-3", "gap", nil},     // too long to be any process's id: malformed, not a restart
		{"", "", nil},                                    // no id: live only
		{"garbage", "gap", nil},                          // no dash
		{nonce + "-", "gap", nil},                        // empty seq
		{nonce + "-12x", "gap", nil},                     // non-digit
		{nonce + "--3", "gap", nil},                      // sign-ish
		{nonce + "-+3", "gap", nil},                      //
		{nonce + "-99999999999999999999999", "gap", nil}, // over-long
		{nonce + "-18446744073709551616", "gap", nil},    // overflows uint64 within the length cap
	} {
		s, _ := sub(t, b, func(r *sse.Subscription) { r.LastEventID = c.id })
		ctx, cancel := context.WithCancel(context.Background())
		rec, done := run(ctx, s)
		if c.wantReset != "" {
			rec.awaitText(t, `"reason":"`+c.wantReset+`"`)
		} else {
			rec.awaitText(t, "core.stream.opened.v1")
			time.Sleep(10 * time.Millisecond)
			if strings.Contains(rec.text(), "core.stream.reset.v1") {
				t.Errorf("%q: unexpected reset:\n%s", c.id, rec.text())
			}
		}
		var want []string
		for _, n := range c.wantSeqs {
			want = append(want, fmt.Sprintf("%s-%d", nonce, n))
		}
		if len(want) > 0 {
			rec.awaitText(t, want[len(want)-1])
		}
		got := ids(rec.text())
		if !slices.Equal(got, want) {
			t.Errorf("%q: replayed ids %v, want %v", c.id, got, want)
		}
		cancel()
		finished(t, done)
	}
}

func TestSubscribe_ResumeOnEmptyRing(t *testing.T) {
	b := broker(t, nil)
	s, _ := sub(t, b, func(r *sse.Subscription) { r.LastEventID = nonce + "-0" })
	rec, done := run(t.Context(), s)
	rec.awaitText(t, "core.stream.opened.v1")
	if strings.Contains(rec.text(), "reset") {
		t.Errorf("a fresh broker reset a client at seq 0:\n%s", rec.text())
	}
	s2, _ := sub(t, b, func(r *sse.Subscription) { r.LastEventID = nonce + "-1" })
	rec2, done2 := run(t.Context(), s2)
	rec2.awaitText(t, `"reason":"gap"`)
	b.Close()
	finished(t, done)
	finished(t, done2)
}

func TestSubscribe_ReplayHonoursInterestAndKey(t *testing.T) {
	b := broker(t, nil)
	pub(b, "a.x", "k1")
	pub(b, "b.other", "k1") // not in the interest set
	pub(b, "a.y", "k2")
	pub(b, "a.x", "k2")
	s, _ := sub(t, b, func(r *sse.Subscription) { r.LastEventID = nonce + "-0"; r.Key = "k2" })
	rec, done := run(t.Context(), s)
	rec.awaitText(t, nonce+"-4")
	if got := ids(rec.text()); !slices.Equal(got, []string{nonce + "-3", nonce + "-4"}) {
		t.Errorf("ids = %v", got)
	}
	b.Close()
	finished(t, done)
}

func TestSubscribe_NoEntryReplayedTwiceOrMissed(t *testing.T) {
	b := broker(t, func(o *sse.Options) { o.RingSize = 512; o.ClientBuffer = 512 })
	for range 100 {
		pub(b, "a.x", "")
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				pub(b, "a.x", "")
				time.Sleep(50 * time.Microsecond)
			}
		}
	})
	s, _ := sub(t, b, func(r *sse.Subscription) { r.LastEventID = nonce + "-50" })
	rec, done := run(t.Context(), s)
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	b.Close()
	finished(t, done)
	got := ids(rec.text())
	for i, id := range got {
		if want := fmt.Sprintf("%s-%d", nonce, 51+i); id != want {
			t.Fatalf("frame %d = %s, want %s (a gap or a duplicate at the replay/live seam)", i, id, want)
		}
	}
	if len(got) < 50 {
		t.Errorf("only %d frames", len(got))
	}
}

func TestSubscribe_Caps(t *testing.T) {
	b := broker(t, nil) // 4 clients, 2 per bucket
	open := func(bucket string) (*sse.Subscriber, error) {
		return b.Subscribe(sse.Subscription{Bucket: bucket, Topics: []string{"a.x"}, Judge: &judge{}})
	}
	s1, err := open("u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := open("u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := open("u1"); !errors.Is(err, sse.ErrTooManyBucket) {
		t.Errorf("third in a bucket: %v", err)
	}
	if _, err := open("u2"); err != nil {
		t.Errorf("another bucket was refused: %v", err)
	}
	if _, err := open(""); err != nil { // the 4th client, no bucket
		t.Errorf("no-bucket client: %v", err)
	}
	if _, err := open(""); !errors.Is(err, sse.ErrTooManyClients) {
		t.Errorf("fifth client: %v", err)
	}
	if _, err := open("u3"); !errors.Is(err, sse.ErrTooManyClients) {
		t.Errorf("global cap is checked first for every bucket: %v", err)
	}
	s1.Release()
	s1.Release() // idempotent: must not free a second place
	if n := b.Len(); n != 3 {
		t.Fatalf("Len = %d, want 3", n)
	}
	if _, err := open("u1"); err != nil {
		t.Errorf("a released place was not freed: %v", err)
	}
	if _, err := open("u1"); !errors.Is(err, sse.ErrTooManyClients) && !errors.Is(err, sse.ErrTooManyBucket) {
		t.Errorf("double Release freed an extra place: %v", err)
	}
}

func TestSubscribe_AfterClose(t *testing.T) {
	b := broker(t, nil)
	b.Close()
	b.Close() // idempotent
	if _, err := b.Subscribe(sse.Subscription{Judge: &judge{}}); !errors.Is(err, sse.ErrClosed) {
		t.Errorf("Subscribe after Close = %v", err)
	}
	if seq := pub(b, "a.x", ""); seq != 0 {
		t.Errorf("Publish after Close = %d", seq)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.Wait(ctx); err != nil {
		t.Errorf("Wait on an empty closed broker = %v", err)
	}
}

func TestPublish_OverflowDropsQueueAndOwesAReset(t *testing.T) {
	var dropped atomic.Int64
	var resets []string
	var mu sync.Mutex
	b := broker(t, func(o *sse.Options) {
		o.ClientBuffer = 3
		o.Hooks.Dropped = func(n int) { dropped.Add(int64(n)) }
		o.Hooks.Reset = func(r string) { mu.Lock(); resets = append(resets, r); mu.Unlock() }
	})
	s, _ := sub(t, b, nil)
	// Nothing consumes yet: 7 publishes into a queue of 3. The 4th drops 3 and
	// starts afresh, the 7th drops 3 again; entries 7 stays.
	done := make(chan struct{})
	go func() {
		for range 7 {
			pub(b, "a.x", "")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that is not reading")
	}
	if dropped.Load() != 6 {
		t.Errorf("dropped = %d, want 6", dropped.Load())
	}
	rec, run := run(t.Context(), s)
	rec.awaitText(t, nonce+"-7")
	if got := ids(rec.text()); !slices.Equal(got, []string{nonce + "-7"}) {
		t.Errorf("after overflow the queue holds %v, want only the newest", got)
	}
	if !strings.Contains(rec.text(), `"reason":"overflow"`) {
		t.Errorf("no overflow reset:\n%s", rec.text())
	}
	if strings.Index(rec.text(), "core.stream.reset.v1") > strings.Index(rec.text(), nonce+"-7") {
		t.Error("the reset must precede the entries that follow it")
	}
	mu.Lock()
	if !slices.Equal(resets, []string{"overflow"}) {
		t.Errorf("reset hook = %v", resets)
	}
	mu.Unlock()
	b.Close()
	finished(t, run)
}

func TestPublish_OverflowAlsoDropsTheReplay(t *testing.T) {
	var dropped atomic.Int64
	b := broker(t, func(o *sse.Options) {
		o.ClientBuffer = 2
		o.Hooks.Dropped = func(n int) { dropped.Add(int64(n)) }
	})
	for range 5 {
		pub(b, "a.x", "")
	}
	s, _ := sub(t, b, func(r *sse.Subscription) { r.LastEventID = nonce + "-0" }) // replay 5
	for range 3 {
		pub(b, "a.x", "")
	} // queue 2, then the 3rd overflows: drops 2 queued + 5 replayed
	if dropped.Load() != 7 {
		t.Errorf("dropped = %d, want 7", dropped.Load())
	}
	rec, done := run(t.Context(), s)
	rec.awaitText(t, nonce+"-8")
	if got := ids(rec.text()); !slices.Equal(got, []string{nonce + "-8"}) {
		t.Errorf("ids = %v", got)
	}
	b.Close()
	finished(t, done)
}

func TestPublish_DoesNotQueueForUninterestedSubscribers(t *testing.T) {
	var dropped atomic.Int64
	b := broker(t, func(o *sse.Options) {
		o.ClientBuffer = 1
		o.Hooks.Dropped = func(n int) { dropped.Add(int64(n)) }
	})
	s, j := sub(t, b, func(r *sse.Subscription) { r.Topics = []string{"a.x"}; r.Key = "mine" })
	for range 10 {
		pub(b, "a.y", "mine")   // wrong topic
		pub(b, "a.x", "theirs") // wrong key
	}
	if dropped.Load() != 0 {
		t.Errorf("entries nobody wanted overflowed a queue: %d", dropped.Load())
	}
	rec, done := run(t.Context(), s)
	rec.awaitText(t, "core.stream.opened.v1")
	time.Sleep(10 * time.Millisecond)
	if j.deliver.Load() != 0 || len(ids(rec.text())) != 0 {
		t.Errorf("the judge was asked about %d entries nobody wanted", j.deliver.Load())
	}
	b.Close()
	finished(t, done)
}

func TestServe_FrameShapeAndOrder(t *testing.T) {
	b := broker(t, nil)
	pub(b, "a.x", "")
	s, _ := sub(t, b, func(r *sse.Subscription) { r.LastEventID = "otherboot-1" })
	rec, done := run(t.Context(), s)
	pub(b, "a.y", "")
	rec.awaitText(t, nonce+"-2")
	text := rec.text()
	order := []string{"retry: 3000", "core.stream.opened.v1", "core.stream.reset.v1", `id: ` + nonce + `-2`}
	last := -1
	for _, want := range order {
		i := strings.Index(text, want)
		if i <= last {
			t.Errorf("%q out of order in:\n%s", want, text)
		}
		last = i
	}
	if !strings.Contains(text, `"topics":["a.x","a.y"]`) {
		t.Errorf("opened frame lacks the sorted topics:\n%s", text)
	}
	// A control frame never carries an id: it must not move the resume point.
	for block := range strings.SplitSeq(text, "\n\n") {
		if strings.Contains(block, "core.stream.") && strings.Contains(block, "id:") {
			t.Errorf("a control frame carries an id: %q", block)
		}
	}
	rec.mu.Lock()
	if rec.status != http.StatusOK {
		t.Errorf("status %d", rec.status)
	}
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-store", "X-Accel-Buffering": "no"} {
		if rec.hdr.Get(k) != v {
			t.Errorf("%s = %q", k, rec.hdr.Get(k))
		}
	}
	if rec.flushes == 0 {
		t.Error("the stream never flushed")
	}
	rec.mu.Unlock()
	b.Close()
	<-done
}

func TestServe_EveryWriteHasADeadline(t *testing.T) {
	b := broker(t, func(o *sse.Options) { o.WriteTimeout = 3 * time.Second })
	s, _ := sub(t, b, nil)
	rec, done := run(t.Context(), s)
	pub(b, "a.x", "")
	rec.awaitText(t, nonce+"-1")
	rec.mu.Lock()
	n := len(rec.deadlines)
	for _, d := range rec.deadlines {
		if rem := time.Until(d); rem <= 0 || rem > 3*time.Second {
			t.Errorf("deadline %v away, want within (0, 3s]", rem)
		}
	}
	rec.mu.Unlock()
	if n < 2 {
		t.Errorf("%d deadlines for at least two writes", n)
	}
	b.Close()
	finished(t, done)
}

func TestServe_ToleratesAWriterWithoutDeadlines(t *testing.T) {
	b := broker(t, nil)
	s, _ := sub(t, b, nil)
	rec := newRecorder()
	rec.noDeadl = true
	done := make(chan string, 1)
	go func() { done <- s.Serve(t.Context(), rec) }()
	pub(b, "a.x", "")
	rec.awaitText(t, nonce+"-1")
	b.Close()
	if r := finished(t, done); r != sse.CloseShutdown {
		t.Errorf("reason %q", r)
	}
}

func TestServe_WriteFailuresAreClassified(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"deadline": {os.ErrDeadlineExceeded, sse.CloseWriteTimeout},
		"wrapped":  {fmt.Errorf("write: %w", os.ErrDeadlineExceeded), sse.CloseWriteTimeout},
		"reset":    {errors.New("broken pipe"), sse.CloseClient},
	} {
		b := broker(t, nil)
		s, _ := sub(t, b, nil)
		rec, done := run(t.Context(), s)
		rec.awaitText(t, "core.stream.opened.v1")
		rec.failNext(c.err)
		pub(b, "a.x", "")
		if r := finished(t, done); r != c.want {
			t.Errorf("%s: reason %q, want %q", name, r, c.want)
		}
		if b.Len() != 0 {
			t.Errorf("%s: the failed stream is still registered", name)
		}
	}
}

func TestServe_FailedFirstWriteEndsTheStream(t *testing.T) {
	b := broker(t, nil)
	s, _ := sub(t, b, nil)
	rec := newRecorder()
	rec.failNext(os.ErrDeadlineExceeded)
	if r := s.Serve(t.Context(), rec); r != sse.CloseWriteTimeout {
		t.Errorf("reason %q", r)
	}
	if b.Len() != 0 {
		t.Error("still registered")
	}
}

func TestServe_ClientGoneWritesNothingMore(t *testing.T) {
	var closed []string
	var mu sync.Mutex
	b := broker(t, func(o *sse.Options) {
		o.Hooks.Closed = func(r string) { mu.Lock(); closed = append(closed, r); mu.Unlock() }
	})
	s, _ := sub(t, b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	rec, done := run(ctx, s)
	rec.awaitText(t, "core.stream.opened.v1")
	cancel()
	if r := finished(t, done); r != sse.CloseClient {
		t.Errorf("reason %q", r)
	}
	if strings.Contains(rec.text(), "core.stream.closed.v1") {
		t.Error("a closed frame was written to a client that left")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(closed, []string{"client"}) {
		t.Errorf("closed hook = %v", closed)
	}
}

func TestServe_HeartbeatCommentAndBeatFlag(t *testing.T) {
	b := broker(t, func(o *sse.Options) { o.Heartbeat = 15 * time.Millisecond })
	s, j := sub(t, b, nil)
	rec, done := run(t.Context(), s)
	rec.awaitText(t, ": hb\n\n")
	if j.beats.Load() == 0 {
		t.Error("Check was never told it was a heartbeat")
	}
	b.Close()
	finished(t, done)
}

func TestServe_CheckEndsTheStreamWithItsReason(t *testing.T) {
	b := broker(t, nil)
	s, j := sub(t, b, nil)
	rec, done := run(t.Context(), s)
	rec.awaitText(t, "core.stream.opened.v1")
	j.close.Store(sse.CloseCredentialRevoked)
	b.WakeAll()
	if r := finished(t, done); r != sse.CloseCredentialRevoked {
		t.Errorf("reason %q", r)
	}
	if !strings.Contains(rec.text(), `"reason":"credential_revoked"`) {
		t.Errorf("no closed frame:\n%s", rec.text())
	}
}

func TestWakeAll_RunsEveryStreamsCheck(t *testing.T) {
	b := broker(t, func(o *sse.Options) { o.MaxClients = 3 })
	var js []*judge
	var dones []<-chan string
	for range 3 {
		s, j := sub(t, b, nil)
		rec, d := run(t.Context(), s)
		rec.awaitText(t, "core.stream.opened.v1")
		js = append(js, j)
		dones = append(dones, d)
	}
	time.Sleep(20 * time.Millisecond)
	before := make([]int64, 3)
	for i, j := range js {
		before[i] = j.checks.Load()
	}
	b.WakeAll()
	deadline := time.Now().Add(5 * time.Second)
	for i, j := range js {
		for j.checks.Load() <= before[i] && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if j.checks.Load() <= before[i] {
			t.Errorf("stream %d's Check never ran after WakeAll", i)
		}
	}
	b.Close()
	for _, d := range dones {
		finished(t, d)
	}
}

func TestEnd_MatchesOnlyItsTargetsAndFirstReasonWins(t *testing.T) {
	b := broker(t, nil)
	mk := func(meta string) (*recorder, <-chan string) {
		s, _ := sub(t, b, func(r *sse.Subscription) { r.Meta = meta })
		rec, d := run(t.Context(), s)
		rec.awaitText(t, "core.stream.opened.v1")
		return rec, d
	}
	recA, doneA := mk("alfred")
	recB, doneB := mk("bruce")
	n := b.End(func(m any) bool { return m == "alfred" }, sse.CloseCredentialRevoked)
	if n != 1 {
		t.Errorf("End signalled %d", n)
	}
	if r := finished(t, doneA); r != sse.CloseCredentialRevoked {
		t.Errorf("alfred: %q", r)
	}
	recA.awaitText(t, `"reason":"credential_revoked"`)
	if strings.Contains(recB.text(), "closed") {
		t.Error("an unmatched stream was ended")
	}
	if n := b.End(func(any) bool { return true }, sse.ClosePermissionRevoked); n != 1 {
		t.Errorf("second End signalled %d", n)
	}
	if n := b.End(func(any) bool { return true }, sse.ClosePermissionRevoked); n != 0 && n != 1 {
		t.Errorf("third End = %d", n)
	}
	if r := finished(t, doneB); r != sse.ClosePermissionRevoked {
		t.Errorf("bruce: %q", r)
	}
}

func TestEnd_FirstReasonWins(t *testing.T) {
	b := broker(t, nil)
	s, _ := sub(t, b, nil)
	// Not served yet: both calls land before the stream reads either.
	b.End(func(any) bool { return true }, sse.CloseCredentialRevoked)
	if n := b.End(func(any) bool { return true }, sse.ClosePermissionRevoked); n != 0 {
		t.Errorf("a second End on an ended stream signalled %d", n)
	}
	rec := newRecorder()
	if r := s.Serve(t.Context(), rec); r != sse.CloseCredentialRevoked {
		t.Errorf("reason %q, want the first", r)
	}
}

func TestEnd_StopsQueueingAndFramesNothingAfter(t *testing.T) {
	b := broker(t, nil)
	s, _ := sub(t, b, nil)
	b.End(func(any) bool { return true }, sse.CloseShutdown)
	pub(b, "a.x", "")
	rec := newRecorder()
	s.Serve(t.Context(), rec)
	if len(ids(rec.text())) != 0 {
		t.Errorf("an ended stream still delivered:\n%s", rec.text())
	}
}

func TestClose_EndsEveryStreamAndWaitReturns(t *testing.T) {
	b := broker(t, nil)
	var dones []<-chan string
	for range 3 {
		s, _ := sub(t, b, nil)
		rec, d := run(t.Context(), s)
		rec.awaitText(t, "core.stream.opened.v1")
		dones = append(dones, d)
	}
	b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Wait(ctx); err != nil {
		t.Fatalf("Wait = %v", err)
	}
	for _, d := range dones {
		if r := finished(t, d); r != sse.CloseShutdown {
			t.Errorf("reason %q", r)
		}
	}
	if b.Len() != 0 {
		t.Errorf("Len = %d", b.Len())
	}
}

func TestWait_TimesOutWithAStreamOpen(t *testing.T) {
	b := broker(t, nil)
	sub(t, b, nil) // registered, never served: it never leaves
	b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := b.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "1 stream") {
		t.Errorf("Wait = %v", err)
	}
}

func TestServe_FramesAreOneDataLine(t *testing.T) {
	cases := map[string]struct {
		data     string
		want     string // substring that must appear; "" means withheld
		withheld bool
	}{
		"pretty":    {"{\n  \"a\": 1,\n  \"b\": [1,\n 2]\n}", `data: {"a":1,"b":[1,2]}`, false},
		"crlf":      {"{\"a\":\r\n1}", `data: {"a":1}`, false},
		"forged":    {"{\"a\":1}\n\nid: 999\ndata: {}", "", true},
		"truncated": {"{\"a\":\n", "", true},
		"plain":     {`{"a":1}`, `data: {"a":1}`, false},
	}
	for name, c := range cases {
		b := broker(t, nil)
		s, j := sub(t, b, nil)
		j.data = func(e *sse.Entry) []byte { return []byte(c.data) }
		rec, done := run(t.Context(), s)
		rec.awaitText(t, "core.stream.opened.v1")
		pub(b, "a.x", "")
		if c.withheld {
			time.Sleep(30 * time.Millisecond)
			if strings.Contains(rec.text(), "id: ") || strings.Contains(rec.text(), "999") {
				t.Errorf("%s: forged or broken data reached the wire:\n%s", name, rec.text())
			}
		} else {
			rec.awaitText(t, c.want)
			if got := ids(rec.text()); len(got) != 1 {
				t.Errorf("%s: ids %v", name, got)
			}
		}
		// Every non-blank line of the stream is a recognised field: nothing
		// the payload held became a field of its own.
		for l := range strings.SplitSeq(rec.text(), "\n") {
			if l != "" && !strings.HasPrefix(l, "data: ") && !strings.HasPrefix(l, "id: ") && !strings.HasPrefix(l, "retry: ") && !strings.HasPrefix(l, ": ") {
				t.Errorf("%s: stray line %q", name, l)
			}
		}
		b.Close()
		finished(t, done)
	}
}

func TestServe_ManyEntriesAreBatchedAndAllDelivered(t *testing.T) {
	b := broker(t, func(o *sse.Options) { o.RingSize = 300; o.ClientBuffer = 300 })
	s, _ := sub(t, b, nil)
	for range 250 {
		pub(b, "a.x", "")
	}
	rec, done := run(t.Context(), s)
	rec.awaitText(t, nonce+"-250")
	got := ids(rec.text())
	if len(got) != 250 {
		t.Fatalf("%d frames", len(got))
	}
	for i, id := range got {
		if id != fmt.Sprintf("%s-%d", nonce, i+1) {
			t.Fatalf("frame %d = %s: out of order", i, id)
		}
	}
	b.Close()
	finished(t, done)
}

func TestServe_WithheldEntriesProduceNoFrame(t *testing.T) {
	b := broker(t, nil)
	s, j := sub(t, b, nil)
	j.refuse = func(e *sse.Entry) bool { return e.Seq%2 == 1 }
	rec, done := run(t.Context(), s)
	rec.awaitText(t, "core.stream.opened.v1")
	for range 4 {
		pub(b, "a.x", "")
	}
	rec.awaitText(t, nonce+"-4")
	if got := ids(rec.text()); !slices.Equal(got, []string{nonce + "-2", nonce + "-4"}) {
		t.Errorf("ids = %v", got)
	}
	b.Close()
	finished(t, done)
}

func TestBroker_OldestAgeAndLen(t *testing.T) {
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	b := broker(t, func(o *sse.Options) {
		o.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	})
	if b.OldestAge() != 0 {
		t.Error("empty ring has an age")
	}
	pub(b, "a.x", "")
	mu.Lock()
	now = now.Add(90 * time.Second)
	mu.Unlock()
	pub(b, "a.x", "")
	if got := b.OldestAge(); got != 90*time.Second {
		t.Errorf("OldestAge = %v", got)
	}
	for range 8 { // the ring (8) rolls past the first entry
		pub(b, "a.x", "")
	}
	if got := b.OldestAge(); got != 0 {
		t.Errorf("after rolling, oldest = %v", got)
	}
	if b.Len() != 0 {
		t.Errorf("Len = %d", b.Len())
	}
}

func TestPublish_ConcurrentRaceFree(t *testing.T) {
	b := broker(t, func(o *sse.Options) { o.MaxClients = 8; o.MaxPerBucket = 8; o.ClientBuffer = 16 })
	var wg sync.WaitGroup
	var dones []<-chan string
	for range 6 {
		s, _ := sub(t, b, nil)
		_, d := run(t.Context(), s)
		dones = append(dones, d)
	}
	for range 4 {
		wg.Go(func() {
			for range 200 {
				pub(b, "a.x", "")
			}
		})
	}
	wg.Go(func() { ; b.WakeAll(); b.Len(); b.OldestAge() })
	wg.Wait()
	b.Close()
	for _, d := range dones {
		finished(t, d)
	}
}

// TestEnd_AnEndedStreamQueuesNothingMore: once a stream is ended, later
// publishes neither fill its queue nor count as overflow drops, however many
// arrive before its goroutine returns.
func TestEnd_AnEndedStreamQueuesNothingMore(t *testing.T) {
	var dropped atomic.Int64
	b := broker(t, func(o *sse.Options) {
		o.ClientBuffer = 2
		o.Hooks.Dropped = func(n int) { dropped.Add(int64(n)) }
	})
	s, _ := sub(t, b, nil)
	b.End(func(any) bool { return true }, sse.CloseCredentialRevoked)
	for range 10 {
		pub(b, "a.x", "")
	}
	if dropped.Load() != 0 {
		t.Errorf("an ended stream counted %d overflow drops", dropped.Load())
	}
	s.Release()
}

func TestSetInterest_StopsQueueingARemovedTopic(t *testing.T) {
	var dropped atomic.Int64
	b := broker(t, func(o *sse.Options) {
		o.ClientBuffer = 2
		o.Hooks.Dropped = func(n int) { dropped.Add(int64(n)) }
	})
	s, _ := sub(t, b, nil) // a.x and a.y
	if got := s.Topics(); !slices.Equal(got, []string{"a.x", "a.y"}) {
		t.Fatalf("Topics = %v", got)
	}
	s.SetInterest([]string{"a.y"})
	if got := s.Topics(); !slices.Equal(got, []string{"a.y"}) {
		t.Fatalf("Topics after narrowing = %v", got)
	}
	for range 20 {
		pub(b, "a.x", "") // removed: must neither queue nor overflow
	}
	if dropped.Load() != 0 {
		t.Errorf("a removed topic overflowed the queue: %d drops", dropped.Load())
	}
	pub(b, "a.y", "")
	rec, done := run(t.Context(), s)
	rec.awaitText(t, nonce+"-21")
	if got := ids(rec.text()); !slices.Equal(got, []string{nonce + "-21"}) {
		t.Errorf("ids = %v", got)
	}
	if strings.Contains(rec.text(), "core.stream.reset.v1") {
		t.Errorf("a reset for a topic the stream no longer wants:\n%s", rec.text())
	}
	b.Close()
	finished(t, done)
}

func TestRequestReset_IsWrittenBeforeTheNextEntry(t *testing.T) {
	b := broker(t, nil)
	s, _ := sub(t, b, nil)
	rec, done := run(t.Context(), s)
	rec.awaitText(t, "core.stream.opened.v1")
	s.RequestReset(sse.ResetGap)
	s.RequestReset(sse.ResetRestart) // one is owed: the first reason stands
	rec.awaitText(t, `"reason":"gap"`)
	pub(b, "a.x", "")
	rec.awaitText(t, nonce+"-1")
	if strings.Count(rec.text(), "core.stream.reset.v1") != 1 || strings.Contains(rec.text(), `"reason":"restart"`) {
		t.Errorf("resets:\n%s", rec.text())
	}
	b.Close()
	finished(t, done)
}

// stuckRecorder blocks every write after the first until its deadline passes.
type stuckRecorder struct {
	*recorder
	mu       sync.Mutex
	deadline time.Time
	moved    chan struct{}
	writes   int
}

func (r *stuckRecorder) SetWriteDeadline(t time.Time) error {
	r.mu.Lock()
	r.deadline = t
	r.mu.Unlock()
	select {
	case r.moved <- struct{}{}:
	default:
	}
	return nil
}

func (r *stuckRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.writes++
	stuck := r.writes > 1
	r.mu.Unlock()
	for stuck {
		r.mu.Lock()
		dl := r.deadline
		r.mu.Unlock()
		if !dl.After(time.Now()) {
			return 0, os.ErrDeadlineExceeded
		}
		select {
		case <-r.moved:
		case <-time.After(time.Until(dl)):
		}
	}
	return r.recorder.Write(p)
}

func TestEnd_InterruptsAWriteInFlight(t *testing.T) {
	for name, end := range map[string]func(b *sse.Broker){
		"End":   func(b *sse.Broker) { b.End(func(any) bool { return true }, sse.CloseCredentialRevoked) },
		"Close": func(b *sse.Broker) { b.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			b := broker(t, func(o *sse.Options) { o.WriteTimeout = time.Minute })
			s, _ := sub(t, b, nil)
			w := &stuckRecorder{recorder: newRecorder(), moved: make(chan struct{}, 1)}
			done := make(chan string, 1)
			go func() { done <- s.Serve(t.Context(), w) }()
			pub(b, "a.x", "") // its write blocks: the client is not reading
			deadline := time.Now().Add(5 * time.Second)
			for {
				w.mu.Lock()
				n := w.writes
				w.mu.Unlock()
				if n >= 2 || time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			start := time.Now()
			end(b)
			r := finished(t, done)
			if time.Since(start) > 3*time.Second {
				t.Errorf("the stream took %v to stop with a write in flight and a 1m deadline", time.Since(start))
			}
			want := sse.CloseCredentialRevoked
			if name == "Close" {
				want = sse.CloseShutdown
			}
			if r != want {
				t.Errorf("reason %q, want the end reason %q, not a write timeout", r, want)
			}
		})
	}
}

// pastRefuser refuses a write deadline that is not in the future, as a writer
// whose connection cannot take an interrupt would.
type pastRefuser struct{ *recorder }

func (r pastRefuser) SetWriteDeadline(t time.Time) error {
	if !t.After(time.Now().Add(time.Second)) {
		return errors.New("deadline refused")
	}
	return r.recorder.SetWriteDeadline(t)
}

func TestEnd_ReportsAnInterruptItCouldNotDeliver(t *testing.T) {
	var got atomic.Value
	b := broker(t, func(o *sse.Options) {
		o.Hooks.InterruptFailed = func(err error) { got.Store(err) }
	})
	s, _ := sub(t, b, nil)
	w := pastRefuser{newRecorder()}
	done := make(chan string, 1)
	go func() { done <- s.Serve(t.Context(), w) }()
	w.awaitText(t, "core.stream.opened.v1")
	b.End(func(any) bool { return true }, sse.CloseCredentialRevoked)
	if r := finished(t, done); r != sse.CloseCredentialRevoked {
		t.Errorf("reason %q", r)
	}
	if err, _ := got.Load().(error); err == nil || !strings.Contains(err.Error(), "deadline refused") {
		t.Errorf("InterruptFailed = %v", err)
	}
}

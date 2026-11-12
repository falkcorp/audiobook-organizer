// file: core/outbox/logs_test.go
// version: 1.0.2
// guid: 7f507577-1d8d-4031-8266-e190b04b52d9
// last-edited: 2026-11-12

package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// logSentinel stands for a payload value. Every scenario below makes the
// failure carry it the way a real handler's would (an error built from the
// message it was processing, a panic holding it, a validator quoting it) and
// asserts that no log line of the relay ever contains it.
const logSentinel = "JOKER-SECRET"

// logNumber is a payload value of another shape, for a decoder that quotes numbers.
const logNumber = "8675309.5"

// logCapture is a slog sink the test reads back.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (c *logCapture) count(sub string) int { return strings.Count(c.text(), sub) }

func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// panicHolder is a panic value whose fmt rendering shows what it holds.
type panicHolder struct{ Villain string }

// A failing handler, a panicking handler, a typed payload that does not decode
// and a raw payload that breaks the topic's schema all dead-letter, and none of
// them puts the payload, an error text or a panic value in the log. The log
// carries the token, the consumer, the topic, the seq, the attempt and the Go
// type of the error or the panic, and a panic's frames name functions only.
func TestOutbox_FailureLogsCarryNoPayload(t *testing.T) {
	handlerFailures := []struct {
		villain string
		fail    func(v string) error
	}{
		{"error", func(v string) error { return fmt.Errorf("cannot process %s", v) }},
		{"permanent", func(v string) error { return fmt.Errorf("%w: %s is on the roof", outbox.ErrPermanent, v) }},
		{"schema", func(v string) error { return fmt.Errorf("%w: %s is wrong", outbox.ErrSchema, v) }},
		{"panic-string", func(v string) error { panic("the " + v + " fell over") }},
		{"panic-error", func(v string) error { panic(errors.New("boom: " + v)) }},
		{"panic-struct", func(v string) error { panic(panicHolder{Villain: v}) }},
	}

	t.Run("handler", func(t *testing.T) {
		logs := &logCapture{}
		r := newRig(t, rigOpts{
			log: logs.logger(),
			cfg: func(c *outbox.Config) { c.MaxAttempts = 2 },
			register: func(testing.TB, *rig) []registration {
				return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
					func(_ context.Context, m outbox.Message[Dispatch]) error {
						for _, f := range handlerFailures {
							if strings.HasPrefix(m.Payload.Villain, f.villain+":") {
								return f.fail(m.Payload.Villain)
							}
						}
						return nil
					})}}}
			},
		})
		for _, f := range handlerFailures {
			r.stage("wayne-manor", Dispatch{Villain: f.villain + ":" + logSentinel})
		}
		eventually(t, "every message to dead-letter", func() bool {
			return logs.count("outbox: message dead-lettered") == len(handlerFailures)
		})
		out := logs.text()
		if strings.Contains(out, logSentinel) {
			t.Fatalf("a payload value reached the log:\n%s", out)
		}
		for _, want := range []string{
			"outbox: delivery failed; will retry", "outbox: handler panicked",
			"consumer=" + consumerN, "topic=" + noticeName, "seq=", "attempt=1", "attempts=",
			"token=error", "token=permanent", "token=schema", "token=panic",
			"err_type=", "panic_type=string", "panic_type=*errors.errorString", "panic_type=outbox_test.panicHolder",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the log lacks %q:\n%s", want, out)
			}
		}
		// The frames name the panicking function and nothing else.
		if !strings.Contains(out, "frames=") || !strings.Contains(out, "outbox_test.TestOutbox_FailureLogsCarryNoPayload") {
			t.Errorf("a panic's log line lacks its frames:\n%s", out)
		}
		letters, _, err := r.ob.ListDead(context.Background(), "", 50)
		must(t, err)
		if len(letters) != len(handlerFailures) {
			t.Fatalf("%d dead letters, want %d", len(letters), len(handlerFailures))
		}
		for _, l := range letters {
			payload := string(l.Payload)
			l.Payload = nil
			if !strings.Contains(payload, logSentinel) {
				t.Errorf("the dead letter lost its payload: %q", payload)
			}
			// What the dead letter records beside the payload is a token and
			// counts, never the handler's error or panic text.
			if rest := fmt.Sprintf("%+v", l); strings.Contains(rest, logSentinel) {
				t.Errorf("the dead letter carries the sentinel outside its payload: %s", rest)
			}
		}
	})

	t.Run("typed payload that does not decode", func(t *testing.T) {
		logs := &logCapture{}
		r := newRig(t, rigOpts{
			log: logs.logger(),
			register: func(testing.TB, *rig) []registration {
				return []registration{{owner: "core", hs: []outbox.Handler{outbox.Consume(notice(), consumerN,
					func(context.Context, outbox.Message[Dispatch]) error { return nil })}}}
			},
		})
		// encoding/json quotes a number it cannot fit ("cannot unmarshal number
		// 8675309.5 into ... of type int"), so the typed decoder's text would
		// carry this one.
		r.injectIntake(noticeName, "01JZZZZZZZZZZZZZZZZZZZZZ02", "wayne-manor",
			[]byte(`{"block": `+logNumber+`}`))
		eventually(t, "the decode failure to dead-letter", func() bool {
			return logs.count("outbox: message dead-lettered") == 1
		})
		out := logs.text()
		if strings.Contains(out, logSentinel) || strings.Contains(out, logNumber) {
			t.Fatalf("a payload value reached the log:\n%s", out)
		}
		if !strings.Contains(out, "token=schema") {
			t.Errorf("the decode failure was not classified as a schema failure:\n%s", out)
		}
	})

	t.Run("raw payload that breaks the schema", func(t *testing.T) {
		logs := &logCapture{}
		r := newRig(t, rigOpts{
			log: logs.logger(),
			register: func(testing.TB, *rig) []registration {
				return []registration{{owner: "core", hs: []outbox.Handler{outbox.ConsumeRaw(noticeName, consumerN,
					func(context.Context, outbox.RawMessage) error { return nil })}}}
			},
		})
		r.injectIntake(noticeName, "01JZZZZZZZZZZZZZZZZZZZZZ03", "wayne-manor",
			[]byte(`{"villain": {"`+logSentinel+`": 1}, "block": "`+logSentinel+`"}`))
		eventually(t, "the schema failure to dead-letter", func() bool {
			return logs.count("outbox: message dead-lettered") == 1
		})
		out := logs.text()
		if strings.Contains(out, logSentinel) {
			t.Fatalf("a payload value reached the log:\n%s", out)
		}
		if !strings.Contains(out, "token=schema") || !strings.Contains(out, "err_type=*fmt.wrapError") {
			t.Errorf("the schema failure's log line is not the opaque one:\n%s", out)
		}
		letters, _, err := r.ob.ListDead(context.Background(), "", 5)
		must(t, err)
		if len(letters) != 1 || letters[0].Token != "schema" {
			t.Fatalf("dead letters %+v", letters)
		}
	})

	// A row that does not decode, at intake or in the sequenced messages, stops
	// the topic (or the pair) with a health signal and one log line, and
	// neither the log nor the health detail quotes the decoder's text: it
	// repeats the value it rejected, which is a payload. The stop is not
	// retried into a stream of warnings.
	t.Run("rows that do not decode", func(t *testing.T) {
		shapes := []struct {
			name   string
			intake bool
			row    string
		}{
			{"intake unknown encoding", true, `zNEWER-BUILD-ENCODING-` + logSentinel},
			{"intake payload syntax", true, `j{"v":1,"id":"X","key":"wayne-manor","payload":{"villain":` + logSentinel + `},"staged_at":"2026-01-01T00:00:00Z","producer":"core"}`},
			{"intake staged_at", true, `j{"v":1,"id":"X","key":"wayne-manor","payload":{"villain":"Joker"},"staged_at":"` + logSentinel + `","producer":"core"}`},
			{"message staged_at", false, `j{"v":1,"id":"X","key":"wayne-manor","payload":{"villain":"Joker"},"staged_at":"` + logSentinel + `","producer":"core"}`},
			{"message key number", false, `j{"v":1,"id":"X","key":` + logNumber + `,"payload":{"villain":"Joker"},"staged_at":"2026-01-01T00:00:00Z","producer":"core"}`},
			{"message unknown encoding", false, `zNEWER-BUILD-ENCODING-` + logSentinel},
		}
		for _, sh := range shapes {
			t.Run(sh.name, func(t *testing.T) {
				logs := &logCapture{}
				r := newRig(t, rigOpts{log: logs.logger(), register: consumeInto(&recorder{}, consumerN)})
				ctx := context.Background()
				// A consumer's cursors start at the topic's high-water mark, so a
				// row staged before they exist is one it never takes.
				eventually(t, "the consumer's cursors", func() bool { return r.cursors(consumerN) == 2 })
				if sh.intake {
					b := r.outKS.NewBatch()
					must(t, b.Set(outboxstore.InKey(noticeName, "00000000000000000000000000"), []byte(sh.row)))
					must(t, b.Commit(ctx, storage.Sync))
					b.Close()
					r.stage("wayne-manor", Dispatch{Villain: "Joker"})
				} else {
					st := outboxstore.New(r.outKS)
					b := st.OpenBatch()
					_, err := st.StageSequence(ctx, b, noticeName, 2, []outboxstore.IntakeRow{{ID: "X", Intake: outboxstore.Intake{
						ID: "X", Key: "wayne-manor", Payload: []byte(`{}`), StagedAt: time.Now(), Producer: "core"}}})
					must(t, err)
					must(t, b.In(r.outKS).Set(outboxstore.MsgKey(noticeName, outboxstore.Partition("wayne-manor", 2), 1), []byte(sh.row)))
					must(t, b.Commit(ctx, storage.Sync))
					b.Close()
					r.ob.Wake() // staged by hand: nothing else tells the relay
				}
				eventually(t, "the stop to be reported", func() bool {
					h := r.healthOf()
					return h.Status == health.StatusDegraded && h.Reason == "delivery_stopped"
				})
				for range 30 {
					r.ob.Wake()
					time.Sleep(5 * time.Millisecond)
				}
				time.Sleep(200 * time.Millisecond)
				h := r.healthOf()
				if !strings.Contains(h.Detail, "corrupt_row") && !strings.Contains(h.Detail, "unknown_version") {
					t.Errorf("the health detail names no cause: %q", h.Detail)
				}
				for _, text := range []string{logs.text(), h.Detail} {
					if strings.Contains(text, logSentinel) || strings.Contains(text, logNumber) {
						t.Fatalf("a row's value reached the log or the health detail:\n%s\n%s", logs.text(), h.Detail)
					}
				}
				if n := logs.count("level=ERROR"); n != 1 {
					t.Errorf("%d error lines for one stop, want 1:\n%s", n, logs.text())
				}
				// The class of the failure is logged as "fault": the run's own
				// logger already stamps every line with the run's "kind".
				if logs.count("fault=corrupt_row")+logs.count("fault=unknown_version") == 0 {
					t.Errorf("no line carries the fault token:\n%s", logs.text())
				}
				for _, line := range strings.Split(logs.text(), "\n") {
					if strings.Count(line, "kind=") > 1 {
						t.Errorf("a line carries two kind attributes: %s", line)
					}
				}
				if n := logs.count("level=WARN"); n > 2 {
					t.Errorf("%d warnings while a stopped topic waits, want a handful at most:\n%s", n, logs.text())
				}
			})
		}
	})
}

// file: core/outbox/logs_test.go
// version: 1.0.0
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

	"github.com/falkcorp/mediacore/outbox"
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
}

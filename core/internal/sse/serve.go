// file: core/internal/sse/serve.go
// version: 1.0.0
// guid: 05e0a1b8-cca8-4e9d-9c48-f3af45a1a26e
// last-edited: 2026-11-11

package sse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"slices"
	"time"
)

// Control topics (05-realtime §4.2): reserved in core's namespace, not in the
// events catalog, delivered to every subscriber whatever its permissions, and
// written without an id: line so they never move the resume point.
const (
	TopicOpened = "core.stream.opened.v1"
	TopicReset  = "core.stream.reset.v1"
	TopicClosed = "core.stream.closed.v1"
)

const (
	// batch is how many entries one wake judges and writes before it flushes
	// and, if more remain, wakes itself again.
	batch = 64
	// flushBytes bounds the bytes buffered between writes.
	flushBytes = 64 << 10
	// retryFrame tells the client to reconnect after 3 s (05-realtime §4.1).
	retryFrame = "retry: 3000\n\n"
)

// controlFrame is the data of a control frame: the same envelope shape as an
// event's, so a plain EventSource parser reads every frame alike.
type controlFrame struct {
	Topic string    `json:"topic"`
	TS    time.Time `json:"ts"`
	Data  any       `json:"data"`
}

// Serve writes the stream to w until the client leaves, a write fails, the
// judge or End or Close ends it, or ctx ends, and returns why (one of the
// Close* constants). It runs on the caller's goroutine and starts none. Every
// response header and the status are set before the first byte. Release is
// called on the way out, so the caller never needs to.
//
// Frames, in order: "retry: 3000", core.stream.opened.v1 with the effective
// topics, core.stream.reset.v1 if the subscribe owed one, then the replay and
// live entries the judge delivers, a ": hb" comment per heartbeat, and a final
// core.stream.closed.v1 for a reason that has one.
func (s *Subscriber) Serve(ctx context.Context, w http.ResponseWriter) (reason string) {
	b := s.b
	defer func() {
		s.Release()
		if h := b.opt.Hooks.Closed; h != nil {
			h(reason)
		}
	}()
	rc := http.NewResponseController(w)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var buf bytes.Buffer
	buf.WriteString(retryFrame)
	s.control(&buf, TopicOpened, map[string]any{"topics": slices.Sorted(maps.Keys(s.interest))})
	if r := s.write(rc, w, &buf); r != "" {
		return r
	}
	hb := time.NewTicker(b.opt.Heartbeat)
	defer hb.Stop()
	s.signal()
	for {
		beat := false
		select {
		case <-ctx.Done():
			return CloseClient
		case <-s.wake:
		case <-hb.C:
			beat = true
		}
		if r := s.judge.Check(ctx, beat); r != "" {
			return s.finish(rc, w, &buf, r)
		}
		reset, end, ents, more := s.take(batch)
		if end != "" {
			return s.finish(rc, w, &buf, end)
		}
		if reset != "" {
			s.control(&buf, TopicReset, map[string]string{"reason": reset})
			if h := b.opt.Hooks.Reset; h != nil {
				h(reset)
			}
		}
		for _, e := range ents {
			data, ok := s.judge.Deliver(ctx, e)
			if !ok {
				continue
			}
			if !s.frame(&buf, b.eventID(e.Seq), data) {
				continue
			}
			if buf.Len() >= flushBytes {
				if r := s.write(rc, w, &buf); r != "" {
					return r
				}
			}
		}
		if beat {
			buf.WriteString(": hb\n\n")
		}
		if r := s.write(rc, w, &buf); r != "" {
			return r
		}
		if more {
			s.signal()
		}
	}
}

// finish writes the closed frame for reason and returns reason.
func (s *Subscriber) finish(rc *http.ResponseController, w http.ResponseWriter, buf *bytes.Buffer, reason string) string {
	s.control(buf, TopicClosed, map[string]string{"reason": reason})
	if r := s.write(rc, w, buf); r != "" {
		return r
	}
	return reason
}

// control appends one control frame, which carries no id: line.
func (s *Subscriber) control(buf *bytes.Buffer, topic string, data any) {
	raw, err := json.Marshal(controlFrame{Topic: topic, TS: s.b.now(), Data: data})
	if err != nil {
		return // the three payloads are maps of strings and cannot fail
	}
	buf.WriteString("data: ")
	buf.Write(raw)
	buf.WriteString("\n\n")
}

// frame appends one event frame. data is one JSON document; a newline in it
// would end the data: line and let the rest of the bytes be read as further
// frame fields, so whitespace between tokens is compacted away (a JSON string
// cannot hold a raw newline) and anything that is not JSON is withheld.
func (s *Subscriber) frame(buf *bytes.Buffer, id string, data []byte) bool {
	if bytes.ContainsAny(data, "\r\n") {
		var c bytes.Buffer
		if json.Compact(&c, data) != nil || bytes.ContainsAny(c.Bytes(), "\r\n") {
			return false
		}
		data = c.Bytes()
	}
	buf.WriteString("id: ")
	buf.WriteString(id)
	buf.WriteString("\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return true
}

// write sends and clears buf under one per-write deadline, then flushes, and
// returns "" on success or the reason the stream is over. A writer that
// cannot take a deadline or a flush (a test recorder) is tolerated; any other
// failure ends the stream.
func (s *Subscriber) write(rc *http.ResponseController, w http.ResponseWriter, buf *bytes.Buffer) string {
	if buf.Len() == 0 {
		return ""
	}
	if err := rc.SetWriteDeadline(time.Now().Add(s.b.opt.WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return CloseClient
	}
	_, err := w.Write(buf.Bytes())
	buf.Reset()
	if err == nil {
		err = rc.Flush()
		if errors.Is(err, http.ErrNotSupported) {
			err = nil
		}
	}
	switch {
	case err == nil:
		return ""
	case errors.Is(err, os.ErrDeadlineExceeded):
		return CloseWriteTimeout
	default:
		return CloseClient
	}
}

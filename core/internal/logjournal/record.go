// file: core/internal/logjournal/record.go
// version: 1.0.0
// guid: 7c3a9e15-4b82-4d06-9f1e-a2d68b5c0e73
// last-edited: 2026-11-13

package logjournal

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage/codec"
)

// RecordVersion is the version of every record this package writes. A row of
// another version decodes to codec.ErrUnknownVersion; Query skips it and counts
// it (Store.Corrupt), because one row of a newer build must not hide the
// journal.
const RecordVersion = 1

// row is the value of c/log/journal/<ULID>: one record, its attributes
// flattened to dotted keys with string, number or bool values (05-logging §5).
// Attributes are a list, not an object, so their order and a repeated key
// survive. The envelope owns the member "v"; no member here may be named so.
type row struct {
	Time      time.Time `json:"t"`
	Level     int       `json:"lv"`
	Module    string    `json:"m,omitempty"`
	Subsystem string    `json:"s,omitempty"`
	Message   string    `json:"msg"`
	Attrs     []rowAttr `json:"a,omitempty"`
	TraceID   string    `json:"tr,omitempty"`
	SpanID    string    `json:"sp,omitempty"`
}

// rowAttr is one flattened attribute; V is a JSON string, number or bool.
type rowAttr struct {
	K string          `json:"k"`
	V json.RawMessage `json:"v"`
}

func rowCodec() codec.Codec[row] { return codec.JSON[row](RecordVersion) }

// errCorruptRow is a row of the right version that does not decode. It never
// carries the decoder's text, which can quote the value it rejected.
var errCorruptRow = errors.New("logjournal: a row does not decode")

// toRow is r as stored, with t as its time (the caller has already replaced a
// zero time and checked the range).
func toRow(r log.Record, t time.Time) (row, error) {
	out := row{
		Time: t.UTC(), Level: int(r.Level), Module: r.Module, Subsystem: r.Subsystem,
		Message: r.Message, TraceID: r.TraceID, SpanID: r.SpanID,
	}
	var err error
	if out.Attrs, err = flatten(nil, "", r.Attrs); err != nil {
		return row{}, err
	}
	return out, nil
}

// flatten appends attrs to out with a group's members under "<group>.<key>".
// A group with an empty key is inlined and an empty attribute dropped, as the
// stdlib handlers do. A LogValuer is resolved; a kind that is not a string,
// number or bool is stored as its text.
func flatten(out []rowAttr, prefix string, attrs []slog.Attr) ([]rowAttr, error) {
	for _, a := range attrs {
		if a.Equal(slog.Attr{}) {
			continue
		}
		v := a.Value.Resolve()
		key := a.Key
		if prefix != "" && key != "" {
			key = prefix + "." + key
		} else if prefix != "" {
			key = prefix
		}
		var raw []byte
		var err error
		switch v.Kind() {
		case slog.KindGroup:
			g := v.Group()
			if len(g) == 0 {
				continue
			}
			if out, err = flatten(out, key, g); err != nil {
				return nil, err
			}
			continue
		case slog.KindString:
			raw, err = json.Marshal(v.String())
		case slog.KindInt64:
			raw = strconv.AppendInt(nil, v.Int64(), 10)
		case slog.KindUint64:
			raw = strconv.AppendUint(nil, v.Uint64(), 10)
		case slog.KindFloat64:
			if f := v.Float64(); math.IsNaN(f) || math.IsInf(f, 0) {
				// JSON has no NaN or Inf: keep the text rather than fail the batch.
				raw, err = json.Marshal(strconv.FormatFloat(f, 'g', -1, 64))
			} else {
				raw = strconv.AppendFloat(nil, f, 'g', -1, 64)
			}
		case slog.KindBool:
			raw = strconv.AppendBool(nil, v.Bool())
		case slog.KindTime:
			raw, err = json.Marshal(v.Time().UTC().Format(time.RFC3339Nano))
		default: // duration, any (an error value, a struct): its text
			raw, err = json.Marshal(v.String())
		}
		if err != nil {
			return nil, fmt.Errorf("logjournal: attribute %q: %w", key, err)
		}
		out = append(out, rowAttr{K: key, V: raw})
	}
	return out, nil
}

// record is a stored row as a log.Record: attributes come back flat, under
// their dotted keys, numbers as int64, uint64 or float64 by what they hold.
func (r row) record() (log.Record, error) {
	out := log.Record{
		Time: r.Time, Level: slog.Level(r.Level), Module: r.Module, Subsystem: r.Subsystem,
		Message: r.Message, TraceID: r.TraceID, SpanID: r.SpanID,
	}
	for _, a := range r.Attrs {
		v, err := attrValue(a.V)
		if err != nil {
			return log.Record{}, errCorruptRow
		}
		out.Attrs = append(out.Attrs, slog.Attr{Key: a.K, Value: v})
	}
	return out, nil
}

func attrValue(raw json.RawMessage) (slog.Value, error) {
	if len(raw) == 0 {
		return slog.Value{}, errCorruptRow
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return slog.Value{}, err
		}
		return slog.StringValue(s), nil
	case 't', 'f':
		b, err := strconv.ParseBool(string(raw))
		if err != nil {
			return slog.Value{}, err
		}
		return slog.BoolValue(b), nil
	}
	if i, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
		return slog.Int64Value(i), nil
	}
	if u, err := strconv.ParseUint(string(raw), 10, 64); err == nil {
		return slog.Uint64Value(u), nil
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return slog.Value{}, err
	}
	return slog.Float64Value(f), nil
}

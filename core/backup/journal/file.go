// file: core/backup/journal/file.go
// version: 1.0.0
// guid: b701597d-6d06-4dd4-af78-0e0d6a9983ca
// last-edited: 2026-11-10

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// Owner is the owner of the c/backup/ core keyspace (06 §6.4.10 F4 row: the
// kernel registers StorageSpec under owner backup; backup adopts it in the
// backup flip).
const Owner storage.Owner = "core/backup"

// The c/backup/ keys of this package (05-backup §5B.3.6), relative to the
// keyspace and written as the spec names them. served_boot (the content
// marker) and the restore_hooked/<seq> family are F4-11's writers; they are
// declared here because StorageSpec carries them.
const (
	headKey       = "journal_head" // Serialized: Head {seq, mac}, staged by Append through GetForUpdate
	servedBootKey = "served_boot"  // Serialized: ServedBoot {n, nonce}, every write a GetForUpdate (COR6-6)
	gapPrefix     = "journal_gap/" // journal_gap/<since>: an interval whose revocation the journal may lack
)

// StorageSpec returns the c/backup/ storage.Spec the kernel registers from the
// identity flip (COR5-6; 06 §6.4.10 F4 row): a keyspace whose journal_head and
// served_boot are Serialized, so every write of either carries an Expect, and
// whose other families, journal_gap/<since> and restore_hooked/<seq>, are
// plain writes. No migration: the keyspace is new in R2.
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{headKey, servedBootKey}}
}

// ErrCorrupt reports a journal file whose complete lines do not decode or do
// not number 0, 1, 2, ... without a gap. F4-11's chain verification replaces
// this check with the MAC chain.
var ErrCorrupt = errors.New("journal: the revocation journal is corrupt")

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("journal: the revocation journal is closed")

// FileOptions is what the kernel opens the file journal with (coined: the
// spec names no constructor).
type FileOptions struct {
	// Path is the journal file, <backup dir>/<prefix>revocations.journal
	// (§5B.3.6 row "Where"); the kernel resolves it (F4-11 records it in
	// backup.location.json).
	Path string
	// Keyspace is the c/backup/ handle (StorageSpec registered under Owner).
	Keyspace storage.Keyspace
	// Source is the writing binary's ID, written into a line whose Source is
	// empty (app.BinaryID; R5-7).
	Source string
}

// File is the file-backed RevocationJournal: one JSON line per revocation,
// fsynced before Append returns, with the store's head staged into the
// caller's batch. Safe for concurrent use; appends are serialised.
//
// The line encoding (encodeLine, encodeHeader) and the file open (openJournal)
// each live in one place, and every write goes through writeLine, so F4-11's
// authenticated format extends them without a second writer.
type File struct {
	mu     sync.Mutex
	path   string
	f      lineFile
	size   int64  // bytes of complete, committed-to-disk lines
	last   uint64 // seq of the last line
	ks     storage.Keyspace
	source string
	broken error // a failed append that could not be rolled back: every later Append fails
	closed bool
}

var _ RevocationJournal = (*File)(nil)

// lineFile is what File needs of *os.File (a seam for the failure tests).
type lineFile interface {
	Write(p []byte) (int, error)
	Sync() error
	Truncate(size int64) error
	Close() error
}

// OpenFile opens the journal at o.Path, creating it (mode 0600, header line
// seq 0) when absent or empty. An incomplete last line, left by a crash
// inside a write that never returned and so never let its revocation commit,
// is cut off; a complete line that does not decode, or a seq out of order,
// is ErrCorrupt.
func OpenFile(o FileOptions) (*File, error) {
	if o.Path == "" {
		return nil, errors.New("journal: OpenFile without a path")
	}
	if o.Keyspace.Owner() != Owner {
		return nil, fmt.Errorf("journal: OpenFile needs the %s keyspace, got %q", Owner, o.Keyspace.Owner())
	}
	f, data, err := openJournal(o.Path)
	if err != nil {
		return nil, err
	}
	j := &File{path: o.Path, f: f, ks: o.Keyspace, source: o.Source}
	valid, last, lines, err := scan(data)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s: %w", o.Path, err), f.Close())
	}
	if valid < int64(len(data)) {
		if err := errors.Join(f.Truncate(valid), f.Sync()); err != nil {
			return nil, errors.Join(fmt.Errorf("journal: cut the incomplete last line of %s: %w", o.Path, err), f.Close())
		}
	}
	j.size, j.last = valid, last
	if lines == 0 {
		if err := j.writeLine(encodeHeader()); err != nil {
			return nil, errors.Join(err, f.Close())
		}
		if err := syncDir(filepath.Dir(o.Path)); err != nil {
			return nil, errors.Join(err, f.Close())
		}
	}
	return j, nil
}

// openJournal is the one place the journal file is opened: read-write,
// append-only writes, created 0600, and its current content.
func openJournal(path string) (*os.File, []byte, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("journal: open %s: %w", path, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("journal: read %s: %w", path, err), f.Close())
	}
	return f, data, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", dir, err)
	}
	return errors.Join(d.Sync(), d.Close())
}

// scan walks the complete lines of data: it returns the byte length they
// cover, the last seq and how many there are.
func scan(data []byte) (valid int64, last uint64, lines int, err error) {
	off := 0
	for {
		i := bytes.IndexByte(data[off:], '\n')
		if i < 0 {
			return int64(off), last, lines, nil
		}
		seq, err := decodeSeq(data[off : off+i])
		if err != nil {
			return 0, 0, 0, fmt.Errorf("%w: line %d: %w", ErrCorrupt, lines+1, err)
		}
		if seq != uint64(lines) {
			return 0, 0, 0, fmt.Errorf("%w: line %d has seq %d, want %d", ErrCorrupt, lines+1, seq, lines)
		}
		last, lines, off = seq, lines+1, off+i+1
	}
}

// wireLine is a revocation line's JSON form; the header is {"seq":0}. F4-11
// adds kid and mac here and the header's fields to encodeHeader.
type wireLine struct {
	Seq        uint64     `json:"seq"`
	At         time.Time  `json:"at"`
	Kind       Kind       `json:"kind"`
	Subject    entityJSON `json:"subject"`
	Object     entityJSON `json:"object"`
	Detail     []string   `json:"detail,omitempty"`
	Epoch      int64      `json:"epoch,omitempty"`
	RestoreSeq uint64     `json:"restore_seq,omitempty"`
	Source     string     `json:"source"`
}

// entityJSON is entity.Ref's form inside a line.
type entityJSON struct {
	Module string `json:"module,omitempty"`
	Kind   string `json:"kind,omitempty"`
	ID     string `json:"id,omitempty"`
}

func (e entityJSON) ref() entity.Ref { return entity.Ref(e) }

// encodeLine is the one encoding of a revocation line, newline-terminated.
func encodeLine(r Revocation) ([]byte, error) {
	b, err := json.Marshal(wireLine{
		Seq: r.Seq, At: r.At.UTC(), Kind: r.Kind,
		Subject: entityJSON(r.Subject), Object: entityJSON(r.Object),
		Detail: r.Detail, Epoch: r.Epoch, RestoreSeq: r.RestoreSeq, Source: r.Source,
	})
	if err != nil {
		return nil, fmt.Errorf("journal: encode line %d: %w", r.Seq, err)
	}
	return append(b, '\n'), nil
}

// encodeHeader is line 0. Its fields beyond seq are F4-11's (§5B.3.6 row
// "Format and integrity"); this row invents none.
func encodeHeader() []byte { return []byte(`{"seq":0}` + "\n") }

// decodeLine reads a revocation line back.
func decodeLine(line []byte) (Revocation, error) {
	var w wireLine
	if err := json.Unmarshal(line, &w); err != nil {
		return Revocation{}, err
	}
	return Revocation{Seq: w.Seq, At: w.At, Kind: w.Kind, Subject: w.Subject.ref(), Object: w.Object.ref(),
		Detail: w.Detail, Epoch: w.Epoch, RestoreSeq: w.RestoreSeq, Source: w.Source}, nil
}

// decodeSeq reads a line's seq, which every line (the header included) has.
func decodeSeq(line []byte) (uint64, error) {
	var v struct {
		Seq *uint64 `json:"seq"`
	}
	if err := json.Unmarshal(line, &v); err != nil {
		return 0, err
	}
	if v.Seq == nil {
		return 0, errors.New("no seq")
	}
	return *v.Seq, nil
}

// writeLine is the one writer: it appends line and fsyncs, or rolls the file
// back to its previous length and reports the error.
func (j *File) writeLine(line []byte) error {
	prev := j.size
	n, err := j.f.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.f.Sync()
	}
	if err != nil {
		return errors.Join(fmt.Errorf("journal: append to %s: %w", j.path, err), j.rollback(prev))
	}
	j.size = prev + int64(len(line))
	return nil
}

// rollback cuts the file back to size bytes, so a line whose revocation will
// not be recorded leaves nothing behind. When the cut fails the journal is
// broken: a later Append would reuse a seq, so every one fails instead and
// the callers record gaps.
func (j *File) rollback(size int64) error {
	if err := errors.Join(j.f.Truncate(size), j.f.Sync()); err != nil {
		j.broken = fmt.Errorf("journal: %s may hold a line no store committed and could not be cut back: %w", j.path, err)
		return j.broken
	}
	j.size = size
	return nil
}

// headRecord is c/backup/journal_head's value; MAC is F4-11's.
type headRecord struct {
	Seq uint64 `json:"seq"`
	MAC string `json:"mac,omitempty"`
}

// Append writes r, numbered with the next seq, as one fsynced line before the
// caller commits, then stages c/backup/journal_head = max(head, seq) into b,
// reading the head with GetForUpdate. On any error the line is cut back off
// the file and nothing is staged (an Expect the head read recorded is the one
// exception, when the read itself failed and poisoned b); the caller commits
// its revocation with Gap(b, at).
func (j *File) Append(ctx context.Context, b *storage.Batch, r Revocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil {
		return errors.New("journal: Append without a batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.closed:
		return ErrClosed
	case j.broken != nil:
		return j.broken
	}
	r.Seq = j.last + 1
	if r.Source == "" {
		r.Source = j.source
	}
	line, err := encodeLine(r)
	if err != nil {
		return err
	}
	prev := j.size
	if err := j.writeLine(line); err != nil {
		return err
	}
	if err := j.stageHead(ctx, b, r.Seq); err != nil {
		return errors.Join(fmt.Errorf("journal: stage the head for seq %d: %w", r.Seq, err), j.rollback(prev))
	}
	j.last = r.Seq
	return nil
}

// stageHead binds the c/backup/ handle to b and stages max(head, seq).
func (j *File) stageHead(ctx context.Context, b *storage.Batch, seq uint64) error {
	if err := bind(b, j.ks); err != nil {
		return err
	}
	raw, err := b.GetForUpdate(ctx, j.ks, []byte(headKey))
	var cur headRecord
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(raw, &cur); err != nil {
			return fmt.Errorf("decode %s: %w", headKey, err)
		}
	}
	if seq <= cur.Seq {
		return nil
	}
	v, err := json.Marshal(headRecord{Seq: seq})
	if err != nil {
		return err
	}
	return b.In(j.ks).Set([]byte(headKey), v)
}

// bind joins the c/backup/ handle to b. Bind poisons b instead of binding a
// handle of another store, and b.In would then panic, so it is checked.
func bind(b *storage.Batch, ks storage.Keyspace) error {
	if !slices.Contains(b.Bind(ks).Participants(), Owner) {
		return fmt.Errorf("journal: the batch refused the %s handle (a batch of another store?): %w", Owner, storage.ErrOutsideKeyspace)
	}
	return nil
}

// gapRecord is a journal_gap/<since> value.
type gapRecord struct {
	Since  time.Time `json:"since"`
	Reason string    `json:"reason"`
}

// gapKey is journal_gap/<since> with since in fixed-width UTC, so the keys
// sort by time.
func gapKey(since time.Time) []byte {
	return []byte(gapPrefix + since.UTC().Format("2006-01-02T15:04:05.000000000Z"))
}

// Gap stages c/backup/journal_gap/<since> into b: the interval from since
// may hold a revocation the journal lacks (§5B.3.6 row "Append failure").
// It touches no file, so it works when Append cannot. Health's
// revocation_journal_unwritable and the closing gap line are F4-11's.
func (j *File) Gap(b *storage.Batch, since time.Time) error {
	if b == nil {
		return errors.New("journal: Gap without a batch")
	}
	v, err := json.Marshal(gapRecord{Since: since.UTC(), Reason: "append_failed"})
	if err != nil {
		return err
	}
	if err := bind(b, j.ks); err != nil {
		return err
	}
	return b.In(j.ks).Set(gapKey(since), v)
}

// Close closes the file; later appends fail with ErrClosed.
func (j *File) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	return j.f.Close()
}

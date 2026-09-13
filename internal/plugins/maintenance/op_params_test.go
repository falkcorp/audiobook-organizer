// file: internal/plugins/maintenance/op_params_test.go
// version: 1.0.0
// guid: 5b0e7d2c-3f41-4a8e-9c16-e2a7f4d90b38
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// countingOpsDeps counts OpsStore() calls and hands back nil, so a run that got
// past its params decode is visible (count > 0) and cannot touch any data.
type countingOpsDeps struct {
	fakeDeps
	opsStoreCalls *atomic.Int32
}

func (d countingOpsDeps) OpsStore() OpsStore {
	d.opsStoreCalls.Add(1)
	return nil
}

// rootedDeps gives a run a writable root_dir so the default report path resolves.
type rootedDeps struct {
	fakeDeps
	root string
}

func (d rootedDeps) RootDir() string { return d.root }

// pathPrefixOp is one of the five ops that take a path prefix.
type pathPrefixOp struct {
	name  string
	canon string // the key the struct field is tagged with
	alias string // the other spelling
	// decode runs the op's params type through decodeOpParams and returns the
	// canonical PathPrefix and the alias field after folding.
	decode func(raw json.RawMessage) (prefix, alias string, err error)
	run    func(p *Plugin, raw json.RawMessage) error
}

func pathPrefixOps() []pathPrefixOp {
	ctx := context.Background()
	return []pathPrefixOp{
		{
			name: "mark-missing-files", canon: pathPrefixKeyCamel, alias: pathPrefixKeySnake,
			decode: func(raw json.RawMessage) (string, string, error) {
				var p markMissingParams
				err := decodeOpParams(raw, &p)
				return p.PathPrefix, p.PathPrefixAlias, err
			},
			run: func(p *Plugin, raw json.RawMessage) error { return p.runMarkMissingFiles(ctx, raw, &fakeReporter{}) },
		},
		{
			name: "merge-same-path-dupes", canon: pathPrefixKeyCamel, alias: pathPrefixKeySnake,
			decode: func(raw json.RawMessage) (string, string, error) {
				var p mergeSamePathParams
				err := decodeOpParams(raw, &p)
				return p.PathPrefix, p.PathPrefixAlias, err
			},
			run: func(p *Plugin, raw json.RawMessage) error { return p.runMergeSamePathDupes(ctx, raw, &fakeReporter{}) },
		},
		{
			name: "missing-file-repoint", canon: pathPrefixKeyCamel, alias: pathPrefixKeySnake,
			decode: func(raw json.RawMessage) (string, string, error) {
				var p missingFileRepointParams
				err := decodeOpParams(raw, &p)
				return p.PathPrefix, p.PathPrefixAlias, err
			},
			run: func(p *Plugin, raw json.RawMessage) error { return p.runMissingFileRepoint(ctx, raw, &fakeReporter{}) },
		},
		{
			name: "missing-file-audit", canon: pathPrefixKeySnake, alias: pathPrefixKeyCamel,
			decode: func(raw json.RawMessage) (string, string, error) {
				var p missingFileAuditParams
				err := decodeOpParams(raw, &p)
				return p.PathPrefix, p.PathPrefixAlias, err
			},
			run: func(p *Plugin, raw json.RawMessage) error { return p.runMissingFileAudit(ctx, raw, &fakeReporter{}) },
		},
		{
			name: "missing-file-repair", canon: pathPrefixKeySnake, alias: pathPrefixKeyCamel,
			decode: func(raw json.RawMessage) (string, string, error) {
				var p missingFileRepairParams
				err := decodeOpParams(raw, &p)
				return p.PathPrefix, p.PathPrefixAlias, err
			},
			run: func(p *Plugin, raw json.RawMessage) error { return p.runMissingFileRepair(ctx, raw, &fakeReporter{}) },
		},
	}
}

func mustJSON(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// Either spelling alone, and both spellings agreeing, all land in PathPrefix.
// This is also what keeps stored params of past runs decodable on retry: a row
// stored under either key decodes to the same scope.
func TestPathPrefixParams_AcceptEitherSpelling(t *testing.T) {
	for _, op := range pathPrefixOps() {
		t.Run(op.name, func(t *testing.T) {
			for _, body := range []map[string]any{
				{op.canon: "/lib"},
				{op.alias: "/lib"},
				{op.canon: "/lib", op.alias: "/lib"},
			} {
				prefix, alias, err := op.decode(mustJSON(t, body))
				require.NoError(t, err, "body %v", body)
				require.Equal(t, "/lib", prefix, "body %v", body)
				require.Empty(t, alias, "the alias field must be cleared after folding")
			}
		})
	}
}

// An empty body keeps its old meaning: no filter.
func TestPathPrefixParams_EmptyBodyIsNoFilter(t *testing.T) {
	for _, op := range pathPrefixOps() {
		t.Run(op.name, func(t *testing.T) {
			for _, raw := range []string{"", "  ", "{}", "null"} {
				prefix, _, err := op.decode(json.RawMessage(raw))
				require.NoError(t, err, "body %q", raw)
				require.Empty(t, prefix, "body %q", raw)
			}
		})
	}
}

// Rejected bodies fail the RUN before the store is reached. Driven through each
// op's run function (not decodeOpParams alone) so an op that went back to
// json.Unmarshal would fail here.
func TestPathPrefixParams_RejectedBeforeAnyStoreCall(t *testing.T) {
	for _, op := range pathPrefixOps() {
		t.Run(op.name, func(t *testing.T) {
			cases := []struct {
				name string
				body map[string]any
				want []string // substrings the error must contain
			}{
				{"both spellings differ", map[string]any{op.canon: "/lib", op.alias: "/other"},
					[]string{`"pathPrefix"`, `"path_prefix"`, "/lib", "/other"}},
				// The typo is the ONLY key: missing-file-audit has no "apply", so
				// adding one would be reported first and mask the key under test.
				{"unknown key (typo)", map[string]any{"path_prefx": "/lib"},
					[]string{`"path_prefx"`}},
			}
			for _, tc := range cases {
				var calls atomic.Int32
				p := &Plugin{deps: countingOpsDeps{opsStoreCalls: &calls}}
				err := op.run(p, mustJSON(t, tc.body))
				require.Error(t, err, tc.name)
				for _, w := range tc.want {
					require.Contains(t, err.Error(), w, tc.name)
				}
				require.Zero(t, calls.Load(), "%s: the store must not be reached", tc.name)
			}
		})
	}
}

func TestDecodeOpParams_RejectsTrailingData(t *testing.T) {
	var p missingFileRepointParams
	err := decodeOpParams(json.RawMessage(`{"apply":false} {"apply":true}`), &p)
	require.Error(t, err)
}

// Regression: the exact shape that swept the whole library. missing-file-repoint
// read only "pathPrefix", so {"path_prefix": <tree>, "apply": true} decoded with
// an EMPTY prefix and repointed everywhere. Two recoverable rows, one inside the
// requested tree and one in a sibling tree ("lib2", which also exercises the
// folder-boundary match): only the in-scope row may be written.
func TestRepoint_SnakeCasePathPrefixScopesApply(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib")
	lib2 := filepath.Join(dir, "lib2")

	var cores []database.BookFileCore
	full := map[string][]database.BookFile{}
	for i, root := range []string{lib, lib2} {
		writeFile(t, filepath.Join(root, "Stem - 02.mp3"), 1234)
		broken := filepath.Join(root, "Stem - 2", "35.mp3")
		id, book := []string{"f1", "f2"}[i], []string{"b1", "b2"}[i]
		cores = append(cores, database.BookFileCore{ID: id, BookID: book, FilePath: broken, FileSize: 1234})
		full[book] = []database.BookFile{{ID: id, BookID: book, FilePath: broken, FileSize: 1234}}
	}

	var mu sync.Mutex
	var written []string
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return cores, nil },
		GetAllBooksCoreFunc:     func(int, int) ([]database.BookCore, error) { return nil, nil },
		GetBookFilesFunc:        func(bookID string) ([]database.BookFile, error) { return full[bookID], nil },
		UpdateBookFileFunc: func(_ string, f *database.BookFile) error {
			mu.Lock()
			defer mu.Unlock()
			written = append(written, f.FilePath)
			return nil
		},
	}
	p := &Plugin{deps: rootedDeps{fakeDeps: fakeDeps{store: store}, root: dir}}

	raw := mustJSON(t, map[string]any{"path_prefix": lib, "apply": true})
	require.NoError(t, p.runMissingFileRepoint(context.Background(), raw, &fakeReporter{}))

	require.Equal(t, []string{filepath.Join(lib, "Stem - 02.mp3")}, written,
		"only the row under the requested tree may be repointed")
	for _, w := range written {
		require.False(t, strings.HasPrefix(w, lib2), "row outside the scope was written: %s", w)
	}
}

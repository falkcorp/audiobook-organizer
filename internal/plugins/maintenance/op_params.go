// file: internal/plugins/maintenance/op_params.go
// version: 1.0.0
// guid: 89996e05-3a09-42d5-866b-67006fe7787f
// last-edited: 2026-09-13

package maintenance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// The two spellings of the path-prefix param. Historically three ops
// (mark-missing-files, merge-same-path-dupes, missing-file-repoint) read the
// camelCase key and two (missing-file-audit, missing-file-repair) read the
// snake_case one. encoding/json ignores unknown keys, so sending the "other"
// spelling left PathPrefix EMPTY and an apply swept the WHOLE LIBRARY. Every
// one of the five now accepts both.
const (
	pathPrefixKeyCamel = "pathPrefix"
	pathPrefixKeySnake = "path_prefix"
)

// pathPrefixParams is a params struct that carries PathPrefix under one JSON
// key plus the other spelling in an alias field, and knows how to fold the
// alias into the canonical field.
type pathPrefixParams interface {
	foldPathPrefixAlias() error
}

// decodeOpParams is the single decoder for the params of the five
// path-prefix-scoped ops. It is deliberately NOT used by the rest of the
// package: rejecting unknown keys is a per-op contract, adopted here because a
// mistyped scope key on an op that writes silently became "no filter".
//
//   - Unknown keys are REJECTED (json.Decoder.DisallowUnknownFields), so a typo
//     such as "path_prefx" fails the run instead of widening it. The error text
//     names the key.
//   - Trailing data after the object is rejected too.
//   - An empty body (or JSON null) decodes to the zero value: no filter,
//     report-only, exactly as before.
//   - The path-prefix alias is folded last; both spellings present with
//     different values is an error.
//
// Callers must call this BEFORE any store read or write, so a bad body fails
// the op having done nothing.
func decodeOpParams(raw json.RawMessage, dst pathPrefixParams) error {
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			return err
		}
		var extra json.RawMessage
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return errors.New("unexpected data after the params object")
		}
	}
	return dst.foldPathPrefixAlias()
}

// foldPathPrefixAlias moves the alias spelling into the canonical field. Both
// present and equal is fine; both non-empty and different is an error naming
// both keys, because silently picking one would scope a write to a tree the
// caller may not have meant. The alias is cleared afterwards so nothing can
// read it by mistake.
func foldPathPrefixAlias(canonical, alias *string, canonicalKey, aliasKey string) error {
	if *alias == "" {
		return nil
	}
	if *canonical != "" && *canonical != *alias {
		return fmt.Errorf("conflicting path prefixes: %q is %q but %q is %q; "+
			"both spellings are accepted but they must agree (send only one)",
			canonicalKey, *canonical, aliasKey, *alias)
	}
	*canonical = *alias
	*alias = ""
	return nil
}

func (p *markMissingParams) foldPathPrefixAlias() error {
	return foldPathPrefixAlias(&p.PathPrefix, &p.PathPrefixAlias, pathPrefixKeyCamel, pathPrefixKeySnake)
}

func (p *mergeSamePathParams) foldPathPrefixAlias() error {
	return foldPathPrefixAlias(&p.PathPrefix, &p.PathPrefixAlias, pathPrefixKeyCamel, pathPrefixKeySnake)
}

func (p *missingFileRepointParams) foldPathPrefixAlias() error {
	return foldPathPrefixAlias(&p.PathPrefix, &p.PathPrefixAlias, pathPrefixKeyCamel, pathPrefixKeySnake)
}

func (p *missingFileAuditParams) foldPathPrefixAlias() error {
	return foldPathPrefixAlias(&p.PathPrefix, &p.PathPrefixAlias, pathPrefixKeySnake, pathPrefixKeyCamel)
}

func (p *missingFileRepairParams) foldPathPrefixAlias() error {
	return foldPathPrefixAlias(&p.PathPrefix, &p.PathPrefixAlias, pathPrefixKeySnake, pathPrefixKeyCamel)
}

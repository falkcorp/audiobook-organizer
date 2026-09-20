// file: internal/scanner/ai_parse_journal.go
// version: 1.1.0
// guid: 8d3f4b17-6c92-4e05-a71d-5f0b29ce8a46
// last-edited: 2026-09-20

package scanner

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// aiParseJournalKind namespaces filename-parse entries inside the shared AI
// result journal, beside whisper's own kind. Defined in resultjournal so the
// pruner's AllKinds() covers it by construction.
const aiParseJournalKind = resultjournal.KindLLMFilenameParse

// aiParsePromptVersion is part of every journal key, and MUST be bumped
// whenever the parse prompt, the expected reply shape, or the class of model
// behind it changes.
//
// WHY A HAND-BUMPED CONSTANT. The journal is content-keyed, so without a
// version in the key a prompt change would go on being answered from entries
// produced by the OLD prompt, forever, with nothing reporting a problem. There
// is no automatic way to notice: the input filename is unchanged, so the key is
// unchanged. The same pattern and the same reason as
// unified.FormulaVersion ("noisy-or-v1") in dedup scoring.
//
// The model is deliberately NOT in the key. With ai_endpoints routing on, the
// pool picks the endpoint at call time, so the model is not known at Lookup
// time — it cannot be part of a key that must be computed BEFORE the call. The
// endpoint and model that actually served a result are recorded in the journal
// ENTRY instead (Complete takes them), which is where they are useful for audit
// anyway. A deliberate model change is therefore a prompt-version bump.
const aiParsePromptVersion = "v1"

// journalledParser is an aiBatchParser decorator that serves already-parsed
// filenames from the durable AI result journal and sends only the rest to the
// model.
//
// WHY THIS EXISTS. library.ai-parse is ResumePolicy: ResumeDrop
// (server/library_ai_parse_op.go), so a restart or deploy mid-batch drops the
// operation. Before this decorator that threw away every LLM result the model
// had already produced but that had not yet been applied, and a later scan paid
// for all of it again. Nothing was ever double-APPLIED — saveAIFieldsToPrimary
// fills empty fields only — so what was lost was compute, not data. Compute is
// precisely what six-point-goal item 5 is about.
//
// Note what is NOT changed: ResumeDrop stays. The operation still re-runs from
// scratch; it simply no longer re-asks the model anything it has already been
// asked. That keeps the op's own resume reasoning intact and the blast radius
// inside the parse path.
type journalledParser struct {
	inner   aiBatchParser
	journal *resultjournal.Journal
	log     logger.Logger
	// endpoint and model are recorded on each entry for audit. They describe
	// the CONFIGURED backend, not necessarily the pool member that served a
	// given call, which this interface does not expose.
	endpoint string
	model    string
}

// ParseBatch serves journal hits, sends only misses to the model, and journals
// every fresh result before returning.
func (p *journalledParser) ParseBatch(ctx context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	out := make([]*ai.ParsedMetadata, len(filenames))

	// Positions whose result must still be fetched, in input order.
	misses := make([]int, 0, len(filenames))
	for i, name := range filenames {
		raw, ok, err := p.journal.Lookup(aiParseContentKey(name))
		switch {
		case err != nil:
			// A corrupt entry is an error from Lookup, never a silent miss. Do
			// not fail the batch over it: re-asking the model is always safe
			// and the alternative is a poisoned entry blocking a book forever.
			// WARN, not Debug — a Debug-level swallow is how a broken cache
			// stays invisible in prod, which runs at info.
			p.log.Warn("AI parse journal: lookup failed for %q, re-asking the model: %v", name, err)
			misses = append(misses, i)
		case ok:
			md, derr := decodeParsedMetadata(raw)
			if derr != nil {
				p.log.Warn("AI parse journal: undecodable entry for %q, re-asking the model: %v", name, derr)
				misses = append(misses, i)
				continue
			}
			// md may be nil here: a journalled "the model had nothing". That is
			// a HIT, not a miss, and must not be re-asked.
			out[i] = md
		default:
			misses = append(misses, i)
		}
	}

	if len(misses) == 0 {
		return out, nil
	}

	ask := make([]string, len(misses))
	for i, idx := range misses {
		ask[i] = filenames[idx]
	}

	results, err := p.inner.ParseBatch(ctx, ask)
	if err != nil {
		// Nothing is journalled on a failed batch. In particular a short reply
		// is already a typed ResultCountError from parseBatchMetadataFromJSON
		// rather than a positional guess, precisely so one book's metadata is
		// never written onto another — and writing such a guess into a
		// CONTENT-KEYED journal would make it permanent and self-reproducing.
		return nil, err
	}

	// Defence in depth. ParseBatch's contract is "always exactly len(filenames)
	// entries, nil where the model had nothing" (openai_parser.go). If that ever
	// stops holding, journalling positionally would cache one filename's result
	// under another's key — permanently, and a re-run would reproduce it rather
	// than repair it. So on any length disagreement: return the results (the
	// caller's existing behaviour) but journal NOTHING.
	if len(results) != len(ask) {
		p.log.Warn("AI parse journal: parser returned %d results for %d filenames; journalling nothing for this batch",
			len(results), len(ask))
		return mergeParseResults(out, misses, results), nil
	}

	for i, idx := range misses {
		md := results[i]
		out[idx] = md
		// A nil md IS journalled. The model was asked and had nothing to say,
		// which is a result: re-asking the same filename would produce the same
		// nothing. Skipping it would mean the LEAST useful filenames are the
		// ones re-sent to the model on every single run, forever — the exact
		// waste this decorator exists to remove. A later, better model gets its
		// chance through an aiParsePromptVersion bump, which is what that
		// constant is for.
		//
		// It round-trips as JSON null and decodeParsedMetadata maps null back to
		// a nil result, so a journal hit is indistinguishable from a fresh nil.
		if cerr := p.journal.Complete(aiParseContentKey(filenames[idx]), p.endpoint, p.model, md); cerr != nil {
			// The result is valid and is still returned and applied; only the
			// caching failed. The cost is that a restart re-asks for this one.
			p.log.Warn("AI parse journal: could not record %q: %v", filenames[idx], cerr)
		}
	}
	return out, nil
}

// decodeParsedMetadata turns a stored journal entry back into a result.
//
// A stored JSON null is "the model had nothing for this filename" and must come
// back as a NIL result, not a zero-valued one: unmarshalling null into a struct
// leaves it zero and returning &that would turn "no answer" into an answer of
// empty strings.
//
// The round trip is lossy for ParsedMetadata.numericCoercions, which is
// unexported. That is harmless and already intended: the field exists only so
// logNumericCoercions can report a coercion once, and it clears the field
// afterwards precisely "so a cached copy does not log again"
// (ai/openai_parser.go). Nothing reads it for behaviour.
func decodeParsedMetadata(raw json.RawMessage) (*ai.ParsedMetadata, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var md ai.ParsedMetadata
	if err := json.Unmarshal(raw, &md); err != nil {
		return nil, err
	}
	return &md, nil
}

// mergeParseResults places whatever the parser returned back at the positions
// it was asked about, without assuming the two slices are the same length.
func mergeParseResults(out []*ai.ParsedMetadata, misses []int, results []*ai.ParsedMetadata) []*ai.ParsedMetadata {
	for i, idx := range misses {
		if i >= len(results) {
			break
		}
		out[idx] = results[i]
	}
	return out
}

// aiParseContentKey is the journal key for one filename.
//
// The hashed string is the EXACT text the model is shown: ai_batch_phase.go
// sends filepath.Base(FilePath), not the full path. Keying on the basename is
// therefore correct AND free dedup — two books whose basenames are identical
// would be sent identical input and would get identical output, so sharing one
// entry is not an approximation. Keying on the full path instead would silently
// lose all of that sharing while changing no answer.
func aiParseContentKey(filename string) string {
	return resultjournal.ContentKey(filename, aiParsePromptVersion)
}

// withParseJournal wraps parser so its results survive a restart. It returns
// parser unchanged, and says why, whenever the journal cannot be opened: a
// missing cache must never stop the parse itself.
// The store is a PARAMETER rather than a getStore() call inside, so a test can
// prove the wrap actually happens. database.Store embeds RawKVStore
// (database/store.go:117) so the production store always satisfies KVStore --
// but the narrow fakes most scanner tests install do not, and with a hidden
// getStore() every one of those tests passed while silently exercising the
// UNWRAPPED parser. That is the shape of bug #3335 (a type assertion that
// quietly misses in prod), pointed the other way.
func withParseJournal(parser aiBatchParser, raw any, log logger.Logger) aiBatchParser {
	if parser == nil {
		return parser
	}
	store, ok := raw.(resultjournal.KVStore)
	if !ok {
		log.Warn("AI parse journal: store %T does not expose raw KV; results will NOT survive a restart", raw)
		return parser
	}
	j, err := resultjournal.New(store, aiParseJournalKind)
	if err != nil {
		log.Warn("AI parse journal: could not open (%v); results will NOT survive a restart", err)
		return parser
	}
	cfg := &config.AppConfig
	return &journalledParser{
		inner:    parser,
		journal:  j,
		log:      log,
		endpoint: string(cfg.EffectiveLLMMode()),
		model:    cfg.AIBackend.LocalLLMModel,
	}
}

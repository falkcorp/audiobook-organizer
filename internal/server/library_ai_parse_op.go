// file: internal/server/library_ai_parse_op.go
// version: 1.1.0
// guid: 60e01771-b827-4cf4-b3db-0b4b00bc9389
// last-edited: 2026-08-24

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// libraryAIParseParams carries one queued batch of books to AI-parse.
//
// The books are carried by value rather than by ID because the scan that
// produced them is the only thing that knows which ones came out of metadata
// extraction with empty fields, and it does not have IDs to give: scanner.Book
// has no ID field at all -- it is a pre-persistence value, keyed by FilePath.
// Re-deriving the candidate set from the database later would be a different
// query answering a different question.
type libraryAIParseParams struct {
	Books []scanner.Book `json:"books"`
}

// RegisterLibraryAIParseOp registers the "library.ai-parse" v2 OperationDef and
// wires the scanner's enqueue hook to it.
//
// Registration and wiring live together on purpose. The hook is a package-level
// variable in internal/scanner, so if it were set anywhere else a future edit
// could unregister the op and leave the hook pointing at an EnqueueOp call that
// fails for every scan -- which the scan would absorb by parsing inline, i.e.
// silently reverting to the blocking behaviour this op exists to remove.
func (s *Server) RegisterLibraryAIParseOp(reg *opsregistry.Registry) error {
	if err := reg.RegisterOp(opsregistry.OperationDef{
		ID:              "library.ai-parse",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "library",
		DisplayName:     "AI Filename Parsing",
		Description:     "Parses titles, authors, series and narrators out of filenames using the configured LLM, for books the metadata extractor could not fill in.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		// ResumeDrop, not ResumeRestart. A dropped batch costs nothing
		// permanent: the books keep their empty fields, so the next scan
		// re-nominates exactly the same candidates and queues them again. A
		// restart would re-send LLM requests for the books the batch already
		// finished, which costs tokens to arrive at the same rows.
		ResumePolicy: opsregistry.ResumeDrop,
		// Serialized against itself, and only itself. A scan can queue dozens
		// of these in a single run; with a shared key they form an orderly
		// backlog behind one worker instead of opening dozens of concurrent
		// LLM connections. This is the "just have them queue in the background"
		// behaviour, and it also keeps the inter-batch pacing in
		// ai_batch_phase.go meaningful -- pacing inside one operation is
		// pointless if twenty operations ignore each other.
		//
		// Deliberately NOT "library.scan": sharing that key would make AI
		// parsing block the next scan, which is the exact coupling being
		// removed here.
		ConcurrencyKey: "library.ai-parse",
		// 4h. One batch is aiParseEnqueueChunk (200) books at ai_batch_phase's
		// 20 per LLM call with a 2s delay between calls, so ~10 calls plus 20s
		// of pacing -- minutes, not hours, against a healthy backend. The
		// ceiling is loose enough that only a wedged backend reaches it.
		Timeout: 4 * time.Hour,
		Capabilities: []opsregistry.Capability{
			opsregistry.CapLibraryRead,
			opsregistry.CapLibraryWrite,
			// Both, because newAIParser picks the backend from live config at
			// run time: llm_mode=local goes to the configured Ollama base URL
			// (generic), anything else goes to api.openai.com. Declaring only
			// one would fail whichever deployment chose the other.
			opsregistry.CapNetworkOpenAI,
			opsregistry.CapNetworkGeneric,
		},
		Run: func(ctx context.Context, params json.RawMessage, reporter opsregistry.Reporter) error {
			var p libraryAIParseParams
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return fmt.Errorf("decode params for library.ai-parse: %w", err)
				}
			}
			if len(p.Books) == 0 {
				_ = reporter.UpdateProgress(1, 1, "No books to parse")
				return nil
			}
			_ = reporter.UpdateProgress(0, len(p.Books), fmt.Sprintf("Parsing %d filename(s) with the configured LLM...", len(p.Books)))
			// Bridge the reporter in rather than using logger.New: runAIBatchPhase
			// stamps progress per LLM batch via log.UpdateProgress, and a bare
			// StandardLogger sends those stamps to stdout where the registry
			// cannot see them. The watchdog cancels an op that reports no
			// progress for ProgressTimeout, and a 200-book batch is ~10 LLM calls
			// of up to 30s each -- comfortably past it. This phase is already on
			// record for that failure: it is why library.scan could finish its
			// entire file walk and still be canceled for inactivity.
			opLog := operations.LoggerFromReporter(registryProgressAdapter{r: reporter})
			if err := scanner.RunAIParseForBooks(ctx, p.Books, opLog); err != nil {
				return err
			}
			_ = reporter.UpdateProgress(len(p.Books), len(p.Books), fmt.Sprintf("Parsed %d filename(s)", len(p.Books)))
			return nil
		},
	}); err != nil {
		return err
	}

	// Wire the scanner hook now that the op is registered and enqueueable.
	//
	// Registration and wiring live together on purpose. The hook is a
	// package-level variable in internal/scanner, so if it were set anywhere
	// else a future edit could unregister the op and leave the hook pointing at
	// an EnqueueOp call that fails for every scan -- which the scan absorbs by
	// parsing inline, i.e. silently reverting to the blocking behaviour this op
	// exists to remove.
	//
	// Gated on this being the server's OWN registry, because writing a process
	// global from a registration function is otherwise a trap: op_params_decode_test.go
	// and friends register every op against a zero-value &Server{} and a throwaway
	// registry, and an ungated assignment would leave the live hook pointing at a
	// dead test registry for the rest of the package's test binary.
	//
	// The skip is logged rather than silent. A future change to how server.go
	// hands registrars their registry would stop the wiring, and the only symptom
	// would be scans quietly going back to blocking on the LLM -- a regression
	// with no error anywhere. This line is what makes that visible.
	if s.opRegistry == nil || s.opRegistry != reg {
		logging.Info(context.Background(), "library.ai-parse registered without wiring the scanner enqueue hook (not the server's own registry); scans will parse AI candidates inline")
		return nil
	}
	// The enqueue context is deliberately NOT the scan's ctx. Enqueuing is a
	// short database write, but the scan's context is canceled the moment the
	// scan is canceled -- and a canceled scan is exactly when the last folder's
	// candidates would be lost. Detaching keeps the queued work alive past the
	// scan that nominated it.
	scanner.EnqueueAIParseFn = func(_ context.Context, books []scanner.Book) error {
		enqCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := reg.EnqueueOp(enqCtx, "library.ai-parse", libraryAIParseParams{Books: books})
		return err
	}
	return nil
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterLibraryAIParseOp(reg) })
}

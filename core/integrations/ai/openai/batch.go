// file: core/integrations/ai/openai/batch.go
// version: 1.0.0
// guid: e2b7a9d5-4f30-4c68-b1d4-6a9f3c8e0b25
// last-edited: 2026-11-12

package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// maxBatchListPages bounds FindBatches' walk through the project's batches: at
// 100 a page that is 5,000 batches, far past anything an orphan could still be
// waiting on.
func maxBatchListPages() int { return 50 }

// maxResultLine bounds one line of a batch result file.
func maxResultLine() int { return 16 << 20 }

// batchLine is one request of the JSONL input file.
type batchLine struct {
	CustomID string          `json:"custom_id"`
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Body     json.RawMessage `json:"body"`
}

// resultLine is one line of a batch's output or error file.
type resultLine struct {
	CustomID string `json:"custom_id"`
	Response *struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
	} `json:"response"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// SubmitBatch uploads the items as a JSONL file and creates a chat-completions
// batch over it with a 24 hour window, tagged with tags (the job ID, for the
// router's orphan reconcile). One kind per batch: text.
func (i *instance) SubmitBatch(ctx context.Context, model string, items []ai.BatchItem, tags map[string]string) (ai.BatchHandle, error) {
	var buf bytes.Buffer
	for _, it := range items {
		p, err := chatParams(model, it.Request)
		if err != nil {
			return "", &integrations.Error{Ref: i.ref, Op: "batch submit", Class: integrations.ClassInvalid, Err: fmt.Errorf("item %s: %w", it.CustomID, err)}
		}
		body, err := json.Marshal(p)
		if err != nil {
			return "", &integrations.Error{Ref: i.ref, Op: "batch submit", Class: integrations.ClassInvalid, Err: fmt.Errorf("item %s: %w", it.CustomID, err)}
		}
		line, err := json.Marshal(batchLine{CustomID: it.CustomID, Method: "POST", URL: "/v1/chat/completions", Body: body})
		if err != nil {
			return "", &integrations.Error{Ref: i.ref, Op: "batch submit", Class: integrations.ClassInvalid, Err: err}
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	file, err := i.cl.Files.New(ctx, oai.FileNewParams{
		File:    oai.File(bytes.NewReader(buf.Bytes()), "batch.jsonl", "application/jsonl"),
		Purpose: oai.FilePurposeBatch,
	})
	if err != nil {
		return "", i.fail(ctx, "batch upload", err)
	}
	md := shared.Metadata{}
	maps.Copy(md, tags)
	batch, err := i.cl.Batches.New(ctx, oai.BatchNewParams{
		InputFileID:      file.ID,
		Endpoint:         oai.BatchNewParamsEndpointV1ChatCompletions,
		CompletionWindow: oai.BatchNewParamsCompletionWindow24h,
		Metadata:         md,
	})
	if err != nil {
		// The uploaded input is useless without its batch; do not leave it.
		if _, derr := i.cl.Files.Delete(context.WithoutCancel(ctx), file.ID); derr != nil {
			_ = derr // best effort: the batch error is the one that matters
		}
		return "", i.fail(ctx, "batch create", err)
	}
	return ai.BatchHandle(batch.ID), nil
}

// phaseOf maps the provider's batch status to the contract's phases.
func phaseOf(s oai.BatchStatus) ai.BatchPhase {
	switch s {
	case oai.BatchStatusCompleted:
		return ai.BatchCompleted
	case oai.BatchStatusFailed:
		return ai.BatchFailed
	case oai.BatchStatusExpired:
		return ai.BatchExpired
	case oai.BatchStatusCancelled:
		return ai.BatchCanceled
	}
	return ai.BatchInProgress // validating, in progress, finalizing, cancelling
}

// BatchStatus reports a batch's phase and request counts.
func (i *instance) BatchStatus(ctx context.Context, h ai.BatchHandle) (ai.BatchState, error) {
	b, err := i.cl.Batches.Get(ctx, string(h))
	if err != nil {
		return ai.BatchState{}, i.fail(ctx, "batch status", err)
	}
	return ai.BatchState{
		Phase: phaseOf(b.Status), Total: int(b.RequestCounts.Total),
		Completed: int(b.RequestCounts.Completed), Failed: int(b.RequestCounts.Failed),
	}, nil
}

// BatchResults streams a finished batch's output file, then its error file. The
// files are opened when BatchResults is called, so a batch that cannot be read
// fails here, not midway through the sequence.
func (i *instance) BatchResults(ctx context.Context, h ai.BatchHandle) (iter.Seq2[ai.BatchResult, error], error) {
	b, err := i.cl.Batches.Get(ctx, string(h))
	if err != nil {
		return nil, i.fail(ctx, "batch results", err)
	}
	if phaseOf(b.Status) != ai.BatchCompleted {
		return nil, i.badOutput("batch results", fmt.Sprintf("the batch is %s, not completed", b.Status))
	}
	var files []string
	for _, id := range []string{b.OutputFileID, b.ErrorFileID} {
		if id != "" {
			files = append(files, id)
		}
	}
	var bodies []*bufio.Scanner
	var closers []func()
	cleanup := func() {
		for _, c := range closers {
			c()
		}
	}
	for _, id := range files {
		resp, err := i.cl.Files.Content(ctx, id)
		if err != nil {
			cleanup()
			return nil, i.fail(ctx, "batch results", err)
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), maxResultLine())
		bodies = append(bodies, sc)
		closers = append(closers, func() { drain(resp.Body) })
	}
	return func(yield func(ai.BatchResult, error) bool) {
		defer cleanup()
		for _, sc := range bodies {
			for sc.Scan() {
				var line resultLine
				if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
					yield(ai.BatchResult{}, i.badOutput("batch results", "a result line is not JSON"))
					return
				}
				if !yield(i.result(line), nil) {
					return
				}
			}
			if err := sc.Err(); err != nil {
				yield(ai.BatchResult{}, i.fail(ctx, "batch results", err))
				return
			}
		}
	}, nil
}

// result turns one result line into a BatchResult: the reply, or the provider's
// per-row error as text.
func (i *instance) result(l resultLine) ai.BatchResult {
	r := ai.BatchResult{CustomID: l.CustomID}
	switch {
	case l.Error != nil:
		r.Err = l.Error.Code + ": " + l.Error.Message
	case l.Response == nil:
		r.Err = "no response"
	case l.Response.StatusCode != 200:
		r.Err = fmt.Sprintf("status %d: %s", l.Response.StatusCode, detail(string(l.Response.Body)))
	default:
		var c oai.ChatCompletion
		if err := json.Unmarshal(l.Response.Body, &c); err != nil || len(c.Choices) == 0 {
			r.Err = "the reply is not a chat completion"
			break
		}
		r.Text = &ai.TextResponse{
			Text: c.Choices[0].Message.Content, FinishReason: string(c.Choices[0].FinishReason),
			Model: c.Model, Usage: usageOf(c.Usage),
		}
	}
	return r
}

// CancelBatch cancels a batch at the provider.
func (i *instance) CancelBatch(ctx context.Context, h ai.BatchHandle) error {
	if _, err := i.cl.Batches.Cancel(ctx, string(h)); err != nil {
		return i.fail(ctx, "batch cancel", err)
	}
	return nil
}

// FindBatches lists the provider's batches whose metadata holds every tag (the
// orphan reconcile). The listing is the whole project's, newest first, so a
// walk that hits the page cap is an error: absence from a truncated listing
// proves nothing.
func (i *instance) FindBatches(ctx context.Context, tags map[string]string) ([]ai.BatchHandle, error) {
	page, err := i.cl.Batches.List(ctx, oai.BatchListParams{Limit: param.NewOpt[int64](100)})
	if err != nil {
		return nil, i.fail(ctx, "batch list", err)
	}
	var out []ai.BatchHandle
	for n := 1; page != nil; n++ {
		for _, b := range page.Data {
			if matches(b.Metadata, tags) {
				out = append(out, ai.BatchHandle(b.ID))
			}
		}
		if len(page.Data) == 0 {
			break
		}
		next, err := page.GetNextPage()
		if err != nil {
			return out, i.fail(ctx, "batch list", err)
		}
		if next != nil && n >= maxBatchListPages() {
			return out, i.badOutput("batch list", fmt.Sprintf("the batch listing was cut off after %d pages", n))
		}
		page = next
	}
	return out, nil
}

// matches reports whether md holds every tag.
func matches(md map[string]string, tags map[string]string) bool {
	for k, v := range tags {
		if md[k] != v {
			return false
		}
	}
	return true
}

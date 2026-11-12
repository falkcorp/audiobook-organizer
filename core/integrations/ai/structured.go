// file: core/integrations/ai/structured.go
// version: 1.0.0
// guid: 7c1f4a96-8b23-4d50-a6e8-2d5b9f3c1e47
// last-edited: 2026-11-12

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// structuredPlan is how one GenerateJSON call asks for its answer.
type structuredPlan struct {
	schema  json.RawMessage
	name    string
	strict  bool
	derived bool // the schema came from T, so T's required fields are enforced locally
}

// planStructured picks the schema: the request's own Output.Schema when set,
// else the one derived from T.
func planStructured[T any](c Capability, req TextRequest) (structuredPlan, error) {
	p := structuredPlan{name: strings.NewReplacer(".", "_", "-", "_").Replace(c.id), strict: true}
	if len(req.Output.Schema) > 0 {
		p.schema = req.Output.Schema
		p.strict = req.Output.Strict
		if req.Output.Name != "" {
			p.name = req.Output.Name
		}
		return p, nil
	}
	s, err := schemaFromType[T]()
	if err != nil {
		return p, err
	}
	p.schema, p.derived = s, true
	return p, nil
}

// request shapes req for an endpoint's measured features: a strict JSON Schema
// where FeatureJSONSchema is present, else json_object mode with the schema in
// the system prompt (FeatureJSONMode), else plain text with the schema in the
// prompt. In every case the answer is validated locally, so the features
// change delivery, never eligibility.
func (p structuredPlan) request(req TextRequest, features []Feature) TextRequest {
	out := req
	out.Messages = slices.Clone(req.Messages)
	switch {
	case slices.Contains(features, FeatureJSONSchema):
		out.Output = OutputFormat{Kind: OutputJSONSchema, Name: p.name, Schema: p.schema, Strict: p.strict}
		return out
	case slices.Contains(features, FeatureJSONMode):
		out.Output = OutputFormat{Kind: OutputJSONObject}
	default:
		out.Output = OutputFormat{Kind: OutputText}
	}
	if out.System != "" {
		out.System += "\n\n"
	}
	out.System += "Reply with one JSON object and nothing else. It must conform to this JSON Schema:\n" + string(p.schema)
	return out
}

// repairTurn extends a conversation with the invalid answer and what was wrong
// with it.
func repairTurn(req TextRequest, answer string, problem error) TextRequest {
	out := req
	out.Messages = append(slices.Clone(req.Messages),
		Message{Role: RoleAssistant, Parts: []Part{{Text: answer}}},
		Message{Role: RoleUser, Parts: []Part{{Text: "That answer was not accepted: " + truncate(problem.Error(), 500) +
			"\nReply again with only the corrected JSON object."}}})
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func addUsage(a, b Usage) Usage {
	return Usage{
		Requests:          a.Requests + b.Requests,
		InputTokens:       a.InputTokens + b.InputTokens,
		CachedInputTokens: a.CachedInputTokens + b.CachedInputTokens,
		OutputTokens:      a.OutputTokens + b.OutputTokens,
		AudioSeconds:      a.AudioSeconds + b.AudioSeconds,
	}
}

// GenerateJSON asks for T as JSON. The schema is req.Output.Schema if set, else
// derived from T (core's restricted generator: structs, slices, maps with
// string keys, strings, numbers, bools, `enum:"a,b"` tags). The endpoint is
// asked with a strict schema where it has FeatureJSONSchema, in json mode with
// the schema in the system prompt where it has FeatureJSONMode, and in plain
// text with the same prompt otherwise. The decode is strict (unknown fields
// refused, required fields enforced); on failure the SAME endpoint gets
// core.ai.structured.repair_attempts repair turns, then a *QualityError, which
// does not fail over.
func GenerateJSON[T any](ctx context.Context, r *Router, c Capability, req TextRequest, opts ...CallOption) (T, TextResponse, error) {
	var zero T
	e, err := r.resolve(c)
	if err != nil {
		return zero, TextResponse{}, err
	}
	if e.spec.Kind != KindText {
		return zero, TextResponse{}, fmt.Errorf("ai: GenerateJSON needs a text capability, %s is %s", e.spec.ID, e.spec.Kind)
	}
	plan, err := planStructured[T](c, req)
	if err != nil {
		return zero, TextResponse{}, err
	}
	repairs := r.config().RepairAttempts
	type answer struct {
		val  T
		resp TextResponse
	}
	var tr attemptTrace
	got, err := Call(ctx, r, c, func(ctx context.Context, t Target) (answer, Usage, error) {
		tr.note(t)
		g, ok := t.Backend.(TextGenerator)
		if !ok {
			return answer{}, Usage{}, unsupported(t, "generate")
		}
		cur := plan.request(req, t.Features)
		var spent Usage
		for turn := 0; ; turn++ {
			out, err := g.Generate(ctx, t.Model, cur)
			if err != nil {
				return answer{}, spent, err
			}
			spent = addUsage(spent, out.Usage)
			val, verr := decodeStrict[T](out.Text, plan.derived)
			if verr == nil {
				if turn > 0 {
					r.m.repaired(ctx, e.spec.ID, "repaired")
				}
				out.Usage = spent
				return answer{val: val, resp: out}, spent, nil
			}
			if turn >= repairs {
				if turn > 0 {
					r.m.repaired(ctx, e.spec.ID, "failed")
				}
				// The router records usage only for a success, and these tokens
				// were spent.
				r.m.usage(ctx, e.spec.ID, t.Endpoint, r.modelLabel(t.Model), modeSync, spent)
				return answer{}, spent, Quality(fmt.Errorf("ai: structured output of %s is invalid after %d repair turn(s): %w", e.spec.ID, turn, verr))
			}
			cur = repairTurn(cur, out.Text, verr)
		}
	}, opts...)
	if err != nil {
		return zero, TextResponse{}, err
	}
	got.resp.Served = tr.served()
	return got.val, got.resp, nil
}

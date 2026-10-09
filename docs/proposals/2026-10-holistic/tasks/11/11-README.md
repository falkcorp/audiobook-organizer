<!-- file: docs/proposals/2026-10-holistic/tasks/11/11-README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b0f6c52-3a1e-4d7b-8e44-5c2f0a7d1e63 -->
<!-- last-edited: 2026-10-09 -->

# Task briefs for doc 11 (metrics strategy: OTel and Prometheus)

Source: `docs/proposals/2026-10-holistic/11-metrics-strategy-otel-prometheus.md` §4. Template: `../00-TEMPLATE.md`. Hard requirement (D66): every pre-existing `/metrics` series name keeps appearing; each brief's Acceptance includes the golden-test check.

| Id | Title | Wave | Model | Size | Depends on | Brief |
|---|---|---|---|---|---|---|
| 11-PR1 | `telemetry.Meter`, views, and the `/metrics` series-name golden (69 families) | 0 | opus | M | none | `11-PR1.md` |
| 11-PR2 | OTLP metric reader behind four config keys | 1 | sonnet | M | 11-PR1 | `11-PR2.md` |
| 11-PR3 | aidispatch migration proof | 1 | sonnet | S | 11-PR1 | `11-PR3.md` |
| 11-PR4 | `internal/opsmetrics`, alert and recording rules | 1 | opus | M | 11-PR1 | `11-PR4.md` |
| 11-PR5 | `client_golang` constructor ratchet (baseline 69) | 1 | sonnet | S | 11-PR1 | `11-PR5.md` |
| 11-PR6 | Pipeline metrics migration | 3 | not briefed | S | 11-PR1, 11-PR3 | doc 11 §4 PR 6 |
| 11-PR7 | AI call metrics and `WithAISpan` wiring | 1 (after 11-PR4) | sonnet | M | 11-PR1, 11-PR4, 11-PR3 | `11-PR7.md` |

Notes:
- 11-PR3 lowers the 11-PR5 baseline from 69 to 64; whichever merges second adjusts its number (never upward).
- 11-PR4 and 11-PR7 share `views.go`, `alert-rules.yml` and `AI-REFERENCE.md`; merge them in order.

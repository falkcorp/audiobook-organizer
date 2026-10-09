// file: web/src/components/review/evidence/EvidencePanel.test.tsx
// version: 2.2.0
// guid: 4f8b0d13-97a2-4c65-b83e-1e6a5c9f0d27
// last-edited: 2026-10-09

import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider } from '@mui/material/styles';
import { EvidencePanel } from './EvidencePanel';
import { dedupEvidence, regroupEvidence } from './adapters';
import { appTheme } from '../../../theme';
import type { ConfidenceEvidence, FactsEvidence, WaterfallEvidence } from './types';
import type { DedupScoreBreakdown } from '../../../services/api';

function renderPanel(evidence: Parameters<typeof EvidencePanel>[0]['evidence']) {
  return render(
    <ThemeProvider theme={appTheme} defaultMode="dark">
      <EvidencePanel evidence={evidence} />
    </ThemeProvider>
  );
}

// `raw` and `confidence` are deliberately DIFFERENT on every signal. They were
// the same number in the fixture this replaces, which is exactly how a mapping
// that reads the wrong one of the two survives a green suite.
const confidence: ConfidenceEvidence = {
  kind: 'confidence',
  score: 87.5,
  band: 'HIGH',
  formula: 'v3',
  signals: [
    {
      id: 'exact_file',
      label: 'Exact file hash',
      confidence: 0.99,
      raw: 1,
      detail: 'identical hash',
      // Set explicitly rather than left off: `dedupEvidence` always populates
      // `primary`, so a fixture omitting it exercises a shape production never
      // produces -- and would leave the primary=true path through the panel
      // (no "(supporting)" suffix) untested.
      primary: true,
    },
    {
      id: 'duration',
      label: 'Duration match',
      confidence: 0.35,
      raw: 0.004,
      detail: 'runtimes agree',
      primary: false,
    },
  ],
};

const facts: FactsEvidence = {
  kind: 'facts',
  headline: 'Looks like a multi-disc set.',
  facts: [
    { label: '12 members', hint: 'Files grouped into this hold.' },
    { label: '3/12 runtimes known', hint: 'Most runtimes are unknown.', warn: true },
  ],
};

const waterfall: WaterfallEvidence = {
  kind: 'waterfall',
  score: 0.4,
  steps: [
    { id: 'base', label: 'Title/author match', op: 'base', operand: 0.8, running: 0.8 },
    { id: 'comp', label: 'Compilation penalty', op: 'multiply', operand: 0.5, running: 0.4 },
  ],
};

describe('EvidencePanel dispatch', () => {
  it('renders each kind with the encoding its arithmetic supports', () => {
    const { unmount } = renderPanel(confidence);
    expect(screen.getByTestId('evidence-confidence')).toBeInTheDocument();
    // NO lane draws a share bar any more, dedup included. This assertion was
    // the exact opposite until 2026-09-01, on the belief that the dedup score
    // was a weighted sum. It is `100 * (1 - PROD(1 - confidence)) + SUM(boost)`
    // -- a product, which has no decomposition into shares. This is the
    // regression guard against the bar being reintroduced.
    expect(screen.queryByTestId('evidence-stacked-bar')).not.toBeInTheDocument();
    unmount();

    const facted = renderPanel(facts);
    expect(screen.getByTestId('evidence-facts')).toBeInTheDocument();
    expect(screen.queryByTestId('evidence-stacked-bar')).not.toBeInTheDocument();
    facted.unmount();

    renderPanel(waterfall);
    expect(screen.getByTestId('evidence-waterfall')).toBeInTheDocument();
    // The bar must not follow the waterfall across: a multiplicative factor has
    // no share of a total, and this is the regression that would reintroduce it.
    expect(screen.queryByTestId('evidence-stacked-bar')).not.toBeInTheDocument();
  });

  it('says nothing was recorded rather than rendering an empty box', () => {
    renderPanel(null);
    expect(screen.getByText(/no evidence recorded/i)).toBeInTheDocument();
  });
});

describe('waterfall rendering', () => {
  it('shows each operation in the reviewer’s terms', () => {
    renderPanel(waterfall);
    // 0.80 appears twice -- as the base operand and as the running total after
    // it -- which is correct, not a bug: the first step's result IS its operand.
    expect(screen.getAllByText('0.80')).toHaveLength(2);
    expect(screen.getByText('×0.50')).toBeInTheDocument(); // multiplier, not a share
    expect(screen.getByText('Compilation penalty')).toBeInTheDocument();
  });

  it('flags a breakdown that does not replay to its score', () => {
    // The defect this guards: a stale or hand-built payload whose steps do not
    // derive the number shown. Presenting that as a derivation is worse than
    // showing nothing, so the panel must say so on the surface.
    renderPanel({ ...waterfall, score: 0.9 });
    expect(screen.getByText(/breakdown incomplete/i)).toBeInTheDocument();
  });

  it('does not flag a consistent breakdown', () => {
    renderPanel(waterfall);
    expect(screen.queryByText(/breakdown incomplete/i)).not.toBeInTheDocument();
  });

  it('renders a replace step as a substitution, not a factor', () => {
    const withRerank: WaterfallEvidence = {
      kind: 'waterfall',
      score: 0.9,
      steps: [
        ...waterfall.steps,
        {
          id: 'llm_rerank',
          label: 'LLM rerank',
          op: 'replace',
          operand: 0.9,
          running: 0.9,
          detail: 'rescaled into [0.200, 1.100]',
        },
      ],
    };
    renderPanel(withRerank);
    expect(screen.getByText('= 0.90')).toBeInTheDocument();
    expect(screen.queryByText(/breakdown incomplete/i)).not.toBeInTheDocument();
  });

  it('explains an absent derivation instead of implying a zero score', () => {
    renderPanel({
      kind: 'waterfall',
      score: 1.2,
      steps: [],
      emptyReason: 'This candidate was produced without a recorded derivation.',
    });
    expect(screen.getByText(/without a recorded derivation/i)).toBeInTheDocument();
  });
});

describe('a candidate whose full row is not here yet', () => {
  // The review index serves every candidate WITHOUT its breakdown; the lane
  // fetches each page's full rows afterwards. During that window -- and for
  // ever after it fails -- the candidate has no breakdown for a reason that
  // has nothing to do with the scorer, so the panel must not say it was
  // "produced without a recorded derivation". Three states, three renderings.
  const indexRow = {
    title: 'Cand one',
    author: 'Author one',
    source: 'test-source',
    score: 1.2,
  } as unknown as MetadataCandidate;

  it('shows a loading note, with a progress indicator, while the detail is pending', () => {
    renderPanel(metadataEvidence(indexRow, 'pending'));
    expect(screen.getByText(/loading the score derivation/i)).toBeInTheDocument();
    expect(screen.getByTestId('evidence-loading')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.queryByText(/without a recorded derivation/i)).not.toBeInTheDocument();
  });

  it('says the derivation could not be loaded when the detail fetch failed', () => {
    renderPanel(metadataEvidence(indexRow, 'failed'));
    expect(screen.getByText(/could not be loaded/i)).toBeInTheDocument();
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    expect(screen.queryByText(/without a recorded derivation/i)).not.toBeInTheDocument();
  });

  it('reserves "without a recorded derivation" for a loaded candidate without one', () => {
    renderPanel(metadataEvidence(indexRow, 'loaded'));
    expect(screen.getByText(/without a recorded derivation/i)).toBeInTheDocument();
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    expect(screen.queryByText(/could not be loaded/i)).not.toBeInTheDocument();
  });

  it('defaults to loaded, for callers whose candidates are always full rows', () => {
    expect(metadataEvidence(indexRow)).toEqual(metadataEvidence(indexRow, 'loaded'));
  });

  it('renders a breakdown it has regardless of the detail state', () => {
    // A row the lane already swapped is never reported pending, but a caller
    // that has the steps must never hide them behind a spinner either.
    const full = {
      ...indexRow,
      score_breakdown: {
        score: 1.2,
        steps: [{ id: 'base', label: 'Base similarity', op: 'base', operand: 1.2, running: 1.2 }],
      },
    } as unknown as MetadataCandidate;
    const ev = metadataEvidence(full, 'pending');
    expect(ev.steps).toHaveLength(1);
    expect(ev.loading).toBeUndefined();
    renderPanel(ev);
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    expect(screen.getByText('Base similarity')).toBeInTheDocument();
  });
});

describe('signal colours are theme-driven', () => {
  // The hues carry meaning: they identify which signal a row is. The old panel
  // hardcoded values chosen against white, several of which were unreadable on
  // the dark paper -- a segment nobody can see is the panel failing to say which
  // signal decided the verdict.
  //
  // jsdom does NOT resolve CSS custom properties: it returns the literal
  // `var(--mui-palette-signal-exact_file)` for both schemes, so a computed-style
  // comparison here would pass whether or not the two schemes actually differ.
  // Split the claim into the two halves that can each be checked on their own:
  // the theme really does define different hues, and the component really does
  // reference the variable rather than a baked-in literal. Whether the variable
  // resolves to a legible colour on real paper is a browser-level question, and
  // belongs to the visual harness.
  it('defines distinct hues per colour scheme in the theme', () => {
    const light = appTheme.colorSchemes.light?.palette?.signal;
    const dark = appTheme.colorSchemes.dark?.palette?.signal;
    expect(light).toBeDefined();
    expect(dark).toBeDefined();

    const keys = Object.keys(light!) as Array<keyof typeof light>;
    expect(keys.length).toBeGreaterThan(0);
    for (const key of keys) {
      expect(light![key], `signal hue "${String(key)}" is identical in both schemes`).not.toBe(
        dark![key]
      );
    }
  });

  it('paints segments from the signal variable, not a hardcoded literal', () => {
    render(
      <ThemeProvider theme={appTheme} defaultMode="dark">
        <EvidencePanel evidence={confidence} />
      </ThemeProvider>
    );
    // The row swatches are the surviving consumer of the signal palette now
    // that the stacked bar is gone; the hues still have to tie a row to a kind.
    const swatch = screen.getByTestId('signal-swatch-exact_file');
    const bg = getComputedStyle(swatch).backgroundColor;
    expect(bg).toContain('--mui-palette-signal-');
    expect(bg).not.toMatch(/^#|^rgb/);
  });
});

// ---------------------------------------------------------------------------
// Coverage carried over from the panel this one replaces.
//
// ScoreBreakdownPanel was promoted, not rewritten, so its test cases move here
// rather than being dropped. They now run through `dedupEvidence`, which means
// they cover the adapter as well as the rendering -- the promotion is only
// truthful if the dedup lane still shows exactly what it showed before.
// ---------------------------------------------------------------------------

const dedupBreakdown: DedupScoreBreakdown = {
  score: 97.5,
  band: 'CERTAIN',
  formula: 'v2',
  // Real wire shape (models.Signal JSON tags), no cast. `raw` and `confidence`
  // differ on every row on purpose -- see the note on the fixture above.
  signals: [
    { kind: 'exact_file', raw: 1, confidence: 0.99, evidence: 'identical hash' },
    { kind: 'embedding_high', raw: 0.961, confidence: 0.82, evidence: 'vectors agree' },
    { kind: 'duration', raw: 0.004, confidence: 0.35, evidence: 'runtimes agree' },
  ],
};

describe('dedup lane through the shared panel', () => {
  it('renders the score and band', () => {
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.getByText(/Score: 97\.5/)).toBeInTheDocument();
    expect(screen.getByText('CERTAIN')).toBeInTheDocument();
  });

  it('draws no share bar, because the score is a product and not a sum', () => {
    // This test asserted the presence of the bar until 2026-09-01. The
    // behaviour it protected was the defect: the bar divided each signal's
    // `weight` by the total, and `weight` is not a field the backend has ever
    // sent, so every segment computed to NaN and rendered at 0%.
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.queryByTestId('evidence-stacked-bar')).not.toBeInTheDocument();
    expect(screen.getByTestId('evidence-confidence')).toBeInTheDocument();
  });

  it('shows each signal its calibrated confidence, not its raw measurement', () => {
    // The two are different numbers and only one of them drives the score:
    // models.Signal says "ComposeScore reads this field; Raw is stored for
    // human auditing". Mapping `raw` into the headline slot renders a plausible
    // percentage that means something else entirely, so both are asserted.
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.getByTestId('signal-confidence-exact_file')).toHaveTextContent('99%');
    expect(screen.getByTestId('signal-confidence-embedding_high')).toHaveTextContent('82%');
    expect(screen.getByTestId('signal-confidence-duration')).toHaveTextContent('35%');
    expect(screen.getByTestId('signal-raw-embedding_high')).toHaveTextContent('0.96');
  });

  it('renders signal rows with their human labels', () => {
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.getByText(/Exact file hash/)).toBeInTheDocument();
    expect(screen.getByText(/Embedding \(high\)/)).toBeInTheDocument();
    expect(screen.getByText(/Duration match/)).toBeInTheDocument();
  });

  it('renders the formula tag', () => {
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.getByText('v2')).toBeInTheDocument();
  });

  it('renders the empty state when there are no signals', () => {
    renderPanel(dedupEvidence({ ...dedupBreakdown, signals: [] }));
    expect(screen.getByText(/No signal data available/i)).toBeInTheDocument();
  });

  it('prefers skipped_reason over the generic empty state', () => {
    renderPanel(
      dedupEvidence({ ...dedupBreakdown, signals: [], skipped_reason: 'pre-T015 candidate' })
    );
    expect(screen.getByText(/pre-T015 candidate/i)).toBeInTheDocument();
  });

  it('reports an absent breakdown as unrecorded, not as a zero score', () => {
    renderPanel(dedupEvidence(null));
    expect(screen.getByText(/No score breakdown recorded/i)).toBeInTheDocument();
  });
});

describe('regroup lane through the shared panel', () => {
  it('reuses the existing fact adapter rather than reimplementing it', () => {
    renderPanel(
      regroupEvidence({ members: 12, durationsKnown: 3 }, 'Looks like a multi-disc set.')
    );
    expect(screen.getByText('Looks like a multi-disc set.')).toBeInTheDocument();
    expect(screen.getByText('12 members')).toBeInTheDocument();
    // The known-runtime gap drives insufficient-evidence, so it must stay flagged.
    expect(screen.getByText('3/12 runtimes known')).toBeInTheDocument();
  });

  it('says nothing was recorded for a pre-evidence hold', () => {
    renderPanel(regroupEvidence(undefined));
    expect(screen.getByText(/No evidence recorded for this hold/i)).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// The wire seam.
//
// Everything above tests one link at a time against fixtures written here, in
// this file, by hand -- which is exactly the setup in which the two sides of a
// contract drift apart while every individual test stays green. `MetadataScoreStep`
// in services/api.ts is a hand-maintained mirror of Go's `ScoreStep`, and a
// comment asking editors to keep them in step is not a mechanism.
//
// This payload is different: it is not written here. It is emitted by the real
// Go search path in internal/metafetch/score_breakdown_fixture_test.go and
// checked in. A renamed or dropped JSON tag now breaks the Go test (the fixture
// no longer matches) or this one (the shape no longer adapts) -- rather than
// reaching a reviewer as a panel of confident, empty rows.
// ---------------------------------------------------------------------------

import fixture from './__fixtures__/score_breakdown.json';
import { metadataEvidence } from './adapters';
import { incompleteReason, recomposeWaterfall } from './types';
import type { MetadataCandidate } from '../../../services/api';

describe('metadata lane, end to end from the Go payload', () => {
  // Only the scoring fields matter here; the rest of the candidate is shaped by
  // the search response and is not what this seam is about.
  const candidate = {
    title: 'Mistborn',
    author: 'Brandon Sanderson',
    source: 'test-source',
    score: fixture.score,
    score_breakdown: fixture,
  } as unknown as MetadataCandidate;

  it('adapts real backend JSON into steps that replay to the shipped score', () => {
    const evidence = metadataEvidence(candidate);
    expect(evidence.kind).toBe('waterfall');
    expect(evidence.steps.length).toBeGreaterThanOrEqual(3);

    // The assertion that a field-name mismatch would break: undefined operands
    // recompose to NaN, and NaN is never close to anything.
    const replayed = recomposeWaterfall(evidence.steps);
    expect(Number.isFinite(replayed)).toBe(true);
    expect(replayed).toBeCloseTo(fixture.score, 9);
  });

  it('carries every field the renderer reads across the wire', () => {
    const [first] = metadataEvidence(candidate).steps;
    // Checked individually rather than via a snapshot: a snapshot of a dropped
    // field is just a smaller snapshot, and would be re-blessed on update.
    expect(first.id).toBeTruthy();
    expect(first.label).toBeTruthy();
    expect(['base', 'multiply', 'add', 'replace']).toContain(first.op);
    expect(typeof first.operand).toBe('number');
    expect(typeof first.running).toBe('number');
    expect(first.detail).toBeTruthy();
  });

  it('renders the real payload as a verified derivation, not an incomplete one', () => {
    renderPanel(metadataEvidence(candidate));
    expect(screen.getByTestId('evidence-waterfall')).toBeInTheDocument();
    // The end the whole chain exists for: the reviewer sees the labels the
    // scorer actually recorded, and no incompleteness warning.
    expect(screen.getByText('Rich metadata')).toBeInTheDocument();
    expect(screen.getByText('Runtime comparison')).toBeInTheDocument();
    expect(screen.queryByText(/breakdown incomplete/i)).not.toBeInTheDocument();
  });

  it('flags the payload as incomplete when a field goes missing', () => {
    // Simulates precisely the drift this seam guards: the backend renames
    // `operand`, so the adapter reads undefined. Without the finiteness check in
    // waterfallIsConsistent this renders as a clean, verified, entirely empty
    // explanation -- the worst of the available failure modes, because it looks
    // exactly like success.
    const drifted = {
      ...candidate,
      score_breakdown: {
        ...fixture,
        steps: fixture.steps.map(({ operand: _operand, ...rest }) => rest),
      },
    } as unknown as MetadataCandidate;

    renderPanel(metadataEvidence(drifted));
    expect(screen.getByText(/breakdown incomplete/i)).toBeInTheDocument();

    // It must survive the bad payload rather than throwing: `undefined.toFixed()`
    // would unmount the entire review screen over one malformed row. The step
    // stays on screen, labelled, with its number shown as unreadable.
    expect(screen.getByText('Rich metadata')).toBeInTheDocument();
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });

  it('explains an unreplayable breakdown differently from a mismatched one', () => {
    // These have different causes and different fixes -- a wrong number means
    // the scorer and the recorder disagree; an absent one means the payload and
    // the panel disagree about the shape of a step. Same chip, different cause.
    const drifted = {
      ...candidate,
      score_breakdown: {
        ...fixture,
        steps: fixture.steps.map(({ operand: _operand, ...rest }) => rest),
      },
    } as unknown as MetadataCandidate;

    expect(
      incompleteReason(recomposeWaterfall(metadataEvidence(drifted).steps), fixture.score)
    ).toMatch(/cannot be replayed at all/i);
    // ...whereas a breakdown that replays to the wrong number names both numbers.
    expect(incompleteReason(0.41, 0.9)).toMatch(/replay to 0\.4100, not 0\.9000/);
  });
});

describe('score row zebra striping', () => {
  // The rows are wide -- label hard left, numbers hard right -- so pairing a
  // label with its value means crossing a lot of empty space. Striping is the
  // rail the eye follows, and it only works if it actually ALTERNATES: a
  // uniform background reads as one block again.
  const threeSteps: WaterfallEvidence = {
    kind: 'waterfall',
    score: 0.4,
    steps: [
      { id: 'base', label: 'Title/author match', op: 'base', operand: 0.8, running: 0.8 },
      { id: 'rich', label: 'Rich metadata', op: 'add', operand: 0.1, running: 0.9 },
      { id: 'comp', label: 'Compilation penalty', op: 'multiply', operand: 0.444, running: 0.4 },
    ],
  };

  it('alternates row backgrounds so a label tracks to its own numbers', () => {
    renderPanel(threeSteps);
    const rows = screen.getAllByTestId('evidence-step-row');
    expect(rows).toHaveLength(3);

    const bg = (el: HTMLElement) => getComputedStyle(el).backgroundColor;

    // Adjacent rows must differ, or there is no stripe at all.
    expect(bg(rows[0])).not.toBe(bg(rows[1]));
    // ...and the pattern must repeat every other row rather than, say, fading.
    expect(bg(rows[0])).toBe(bg(rows[2]));
  });

  it('stripes the confidence signal rows on the same cadence', () => {
    renderPanel({
      ...confidence,
      signals: [
        { id: 'exact_file', label: 'Exact file hash', confidence: 1, raw: 1 },
        { id: 'duration', label: 'Duration match', confidence: 0.9, raw: 0.9 },
        { id: 'metadata_fuzzy', label: 'Metadata fuzzy', confidence: 0.8, raw: 0.8 },
      ],
    });
    const rows = screen.getAllByTestId('evidence-signal-row');
    expect(rows).toHaveLength(3);
    const bg = (el: HTMLElement) => getComputedStyle(el).backgroundColor;
    expect(bg(rows[0])).not.toBe(bg(rows[1]));
    expect(bg(rows[0])).toBe(bg(rows[2]));
  });
});

// 2026-09-27: every exact candidate records the rule that created it as a
// non-scoring `exact_rule` signal. The panel said "No score breakdown
// recorded." for every exact pair before this, so nobody could tell why two
// books titled "read by narrator" were on screen.
describe('exact-rule provenance', () => {
  const ruleOnly: DedupScoreBreakdown = {
    score: 0,
    band: '',
    formula: 'v2',
    signals: [
      {
        kind: 'exact_rule',
        rule: 'title_author',
        raw: 0,
        confidence: 0,
        evidence:
          'Same author "Jae" (author id 2) and near-identical titles: "departure from the script" vs "departure from the script" (normalized; edit distance 0, limit 2).',
      },
    ],
  };

  it('shows the rule and its evidence as visible text, not a tooltip', () => {
    renderPanel(dedupEvidence(ruleOnly, 'exact'));
    const block = screen.getByTestId('exact-rule-title_author');
    expect(block).toHaveTextContent('Same author and title');
    expect(block).toHaveTextContent('near-identical titles');
    expect(block).toHaveTextContent('edit distance 0');
    expect(screen.queryByText(/No score breakdown recorded/i)).not.toBeInTheDocument();
  });

  it('does not claim a score of 0 for a rule-only pair', () => {
    renderPanel(dedupEvidence(ruleOnly, 'exact'));
    expect(screen.queryByText(/Score:/)).not.toBeInTheDocument();
    expect(screen.getByText(/Not scored/i)).toBeInTheDocument();
    // The rule is not a confidence row.
    expect(screen.queryAllByTestId('evidence-signal-row')).toHaveLength(0);
  });

  it('says a content rule paired a placeholder-titled pair', () => {
    renderPanel(
      dedupEvidence(
        {
          ...ruleOnly,
          signals: [
            {
              kind: 'exact_rule',
              rule: 'file_hash',
              raw: 1,
              confidence: 0,
              evidence:
                'Identical file content: sha256 4d0fb53d88268abd… (file f-1). Title "read by narrator" / "read by narrator" is a placeholder and was NOT used as evidence.',
            },
          ],
        },
        'exact'
      )
    );
    const block = screen.getByTestId('exact-rule-file_hash');
    expect(block).toHaveTextContent('Identical file content');
    expect(block).toHaveTextContent('was NOT used as evidence');
  });

  it('shows the rule above the confidence rows of a scored pair', () => {
    renderPanel(
      dedupEvidence({
        ...dedupBreakdown,
        signals: [...dedupBreakdown.signals, ...ruleOnly.signals],
      })
    );
    expect(screen.getByTestId('exact-rule-title_author')).toBeInTheDocument();
    expect(screen.getByText(/Score: 97.5/)).toBeInTheDocument();
    expect(screen.getAllByTestId('evidence-signal-row')).toHaveLength(3);
  });

  it('explains an older exact candidate with no breakdown', () => {
    renderPanel(dedupEvidence(null, 'exact'));
    expect(screen.getByText(/Older exact-match candidate/i)).toBeInTheDocument();
    expect(screen.queryByText(/No score breakdown recorded/i)).not.toBeInTheDocument();
  });
});

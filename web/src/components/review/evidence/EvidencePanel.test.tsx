// file: web/src/components/review/evidence/EvidencePanel.test.tsx
// version: 1.1.0
// guid: 4f8b0d13-97a2-4c65-b83e-1e6a5c9f0d27
// last-edited: 2026-08-20

import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider } from '@mui/material/styles';
import { EvidencePanel } from './EvidencePanel';
import { dedupEvidence, regroupEvidence } from './adapters';
import { appTheme } from '../../../theme';
import type { FactsEvidence, WaterfallEvidence, WeightedEvidence } from './types';
import type { DedupScoreBreakdown } from '../../../services/api';

function renderPanel(evidence: Parameters<typeof EvidencePanel>[0]['evidence']) {
  return render(
    <ThemeProvider theme={appTheme} defaultMode="dark">
      <EvidencePanel evidence={evidence} />
    </ThemeProvider>
  );
}

const weighted: WeightedEvidence = {
  kind: 'weighted',
  score: 87.5,
  band: 'HIGH',
  formula: 'v3',
  signals: [
    { id: 'exact_file', label: 'exact_file', value: 1, weight: 0.6, detail: 'identical hash' },
    { id: 'duration', label: 'duration', value: 0.9, weight: 0.4, detail: 'runtimes agree' },
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
    const { unmount } = renderPanel(weighted);
    // A weighted sum is the ONLY kind that may draw a share bar.
    expect(screen.getByTestId('evidence-stacked-bar')).toBeInTheDocument();
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

describe('signal colours are theme-driven', () => {
  // The hues carry meaning: they tie a bar segment to its row. The old panel
  // hardcoded values chosen against white, several of which were unreadable on
  // the dark paper -- a segment nobody can see is the panel failing to say which
  // signal decided the verdict.
  //
  // jsdom does NOT resolve CSS custom properties: it returns the literal
  // `var(--mui-palette-signal-exact_file)` for both schemes, so a computed-style
  // comparison here would pass whether or not the two schemes actually differ.
  // Split the claim into the two halves that can each be checked honestly:
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
        <EvidencePanel evidence={weighted} />
      </ThemeProvider>
    );
    const swatch = screen.getByTestId('evidence-stacked-bar').firstElementChild;
    expect(swatch).toBeTruthy();
    const bg = getComputedStyle(swatch!).backgroundColor;
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
  signals: [
    { kind: 'exact_file', value: 1, weight: 0.5, evidence: 'identical hash', primary: true },
    { kind: 'embedding_high', value: 0.95, weight: 0.3, evidence: 'vectors agree', primary: false },
    { kind: 'duration', value: 0.9, weight: 0.2, evidence: 'runtimes agree', primary: false },
  ],
};

describe('dedup lane through the shared panel', () => {
  it('renders the score and band', () => {
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.getByText(/Score: 97\.5/)).toBeInTheDocument();
    expect(screen.getByText('CERTAIN')).toBeInTheDocument();
  });

  it('renders the stacked bar', () => {
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.getByTestId('evidence-stacked-bar')).toBeInTheDocument();
  });

  it('renders signal rows with their human labels', () => {
    renderPanel(dedupEvidence(dedupBreakdown));
    expect(screen.getByText('Exact file hash')).toBeInTheDocument();
    expect(screen.getByText('Embedding (high)')).toBeInTheDocument();
    expect(screen.getByText('Duration match')).toBeInTheDocument();
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

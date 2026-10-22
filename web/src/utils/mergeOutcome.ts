// file: web/src/utils/mergeOutcome.ts
// version: 1.0.0
// guid: 1e6b4d92-8a37-4c05-b9f1-3d7a2c5e0f84
// last-edited: 2026-10-22

/** A loser the merge left alone, and why (BookMergeOpResult.skipped). */
export interface SkippedMergeLoser {
  book_id: string;
  reason: string;
}

/** The dedup.book-merge operation's result payload. */
export interface BookMergeOpResult {
  keep_id: string;
  merged: number;
  skipped?: SkippedMergeLoser[];
  no_op: boolean;
}

export interface MergeOutcomePlan {
  message: string;
  severity: 'success' | 'warning' | 'error' | 'info';
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

/**
 * What to tell the user once the merge operation has ended. Queueing the merge
 * is not merging: the operation can fail, or skip losers that are already in
 * the trash, and a toast that says "Merged" before it ran hides both.
 *
 * `titles` maps a book id to the title to show for it; an unknown id is shown
 * as the id.
 */
export function describeMergeOutcome(args: {
  status: string;
  errorMessage?: string | null;
  result: BookMergeOpResult | null;
  requested: number;
  titles?: Record<string, string>;
}): MergeOutcomePlan {
  const { status, errorMessage, result, requested, titles = {} } = args;
  if (status !== 'completed') {
    return {
      severity: 'error',
      message: `The merge did not complete${errorMessage ? `: ${errorMessage}` : ` (${status})`}. Nothing was merged.`,
    };
  }
  if (!result) {
    return {
      severity: 'warning',
      message: 'The merge finished but reported no result, so what it did is unknown.',
    };
  }
  const skipped = result.skipped ?? [];
  const name = (id: string) => `"${titles[id] ?? id}"`;
  const skippedText = skipped.map((s) => `${name(s.book_id)} (${s.reason})`).join('; ');
  if (result.no_op || result.merged === 0) {
    const why = skipped.length > 0 ? `: ${skippedText}` : '';
    return { severity: 'info', message: `Nothing was merged${why}.` };
  }
  const base = `Merged ${result.merged} ${plural(result.merged, 'book', 'books')} as versions.`;
  if (skipped.length > 0) {
    return {
      severity: 'warning',
      message: `${base} Skipped ${skipped.length} of ${requested}: ${skippedText}.`,
    };
  }
  return { severity: 'success', message: base };
}

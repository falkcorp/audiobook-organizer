// file: web/src/utils/mergeOutcome.ts
// version: 1.1.0
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
  no_op?: boolean;
  /** the operation failed; the server stores {keep_id, merged: 0, failed, partial, error} */
  failed?: boolean;
  /**
   * with `failed`: the merge changed something before failing (a partial
   * merge or a loser clean-up failure). false: it was refused before any write.
   */
  partial?: boolean;
  error?: string;
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
    const reason = result?.error || errorMessage || '';
    if (result?.partial) {
      return {
        severity: 'error',
        message: `The merge failed part-way and may have partly applied; see the operation for what changed.${reason ? ` ${reason}` : ''}`,
      };
    }
    if (result?.failed) {
      return {
        severity: 'error',
        message: `The merge was refused before changing anything${reason ? `: ${reason}` : ''}. Nothing was merged.`,
      };
    }
    // No stored result: the operation may have written before it stopped, so
    // never claim nothing was merged.
    return {
      severity: 'error',
      message: `The merge did not complete (${status})${errorMessage ? `: ${errorMessage}` : ''}. Check the operation to see what it did.`,
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

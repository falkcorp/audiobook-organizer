// file: web/src/components/collections/QueryHelp.test.tsx
// version: 1.2.0
// guid: d07cc1a4-a7f6-4e0f-986a-580430c3a670
// last-edited: 2026-10-26

import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { useState } from 'react';
import { render, screen, fireEvent, within } from '@testing-library/react';
import { QueryHelp, SortPicker } from './QueryHelp';
import {
  PER_USER_FIELDS,
  PER_USER_NOTE,
  QUERY_FIELD_HELP,
  SORT_FIELDS,
  WORKED_EXAMPLES,
  decodeSort,
  encodeSort,
  applySortChoice,
  queryError,
  sameSortChoice,
  suggestFields,
  usesPerUserField,
} from './queryHelpData';

// vitest runs with cwd = web/, so the Go sources are one level up.
const repoFile = (rel: string) => readFileSync(resolve(process.cwd(), '..', rel), 'utf8');

function Harness({ initial = '' }: { initial?: string }) {
  const [v, setV] = useState(initial);
  return <QueryHelp value={v} onChange={setV} />;
}

const box = () => screen.getByLabelText('Query') as HTMLInputElement;

describe('query help field list', () => {
  const names = QUERY_FIELD_HELP.map((f) => f.name);

  it('lists the book fields, the date fields and the per-user fields', () => {
    expect(names).toEqual([
      'title',
      'author',
      'narrator',
      'series',
      'format',
      'genre',
      'year',
      'duration',
      'date_added',
      'date_modified',
      'read_status',
      'progress_pct',
      'last_played',
    ]);
    expect(PER_USER_FIELDS).toEqual(['read_status', 'progress_pct', 'last_played']);
    for (const f of QUERY_FIELD_HELP) expect(f.example.startsWith(`${f.name}:`)).toBe(true);
  });

  // The web list is derived from the help table, a third copy of what the Go
  // search package (the translator) and the library list filter (audiobooks) each
  // keep. A field in one list only is a query evaluated as one thing in one place and
  // another elsewhere, and for a collection it decides whether the page offers the
  // per-viewer behaviour at all. Go holds its two copies equal (TestPerUserFieldNamesAgree);
  // this holds the third equal to both, and to what docs/search-syntax.md promises.
  it('keeps the per-user field list equal to the Go copies and to the docs', () => {
    const goMapKeys = (src: string, decl: string) => {
      const body = new RegExp(`${decl}[^{]*\\{([\\s\\S]*?)\\n\\}`).exec(src)?.[1] ?? '';
      return [...body.matchAll(/"([a-z_]+)":/g)].map((m) => m[1]).sort();
    };
    const web = [...PER_USER_FIELDS].sort();
    const translator = goMapKeys(
      repoFile('internal/search/bleve_translator.go'),
      'var perUserFieldSet = map\\[string\\]struct\\{\\}'
    );
    const listFilter = goMapKeys(
      repoFile('internal/audiobooks/service_types.go'),
      'var PerUserFieldNames = map\\[string\\]struct\\{\\}'
    );
    expect(translator.length, 'the Go list was found').toBeGreaterThan(0);
    expect(web).toEqual(translator);
    expect(web).toEqual(listFilter);

    const docs = repoFile('docs/search-syntax.md');
    // "`last_played`, `read_status` and `progress_pct` describe one listener."
    const sentence =
      /((?:`[a-z_]+`(?:, | and ))+`[a-z_]+`) describe one listener/.exec(docs)?.[1] ?? '';
    const documented = [...sentence.matchAll(/`([a-z_]+)`/g)].map((m) => m[1]).sort();
    expect(documented, 'the docs name the per-user fields').toEqual(web);
    // Every field the docs table marks "Per user" is one of them.
    const tableRows = [...docs.matchAll(/^\| `([a-z_]+)`[^|\n]*\|[^|\n]*\| Per user \|$/gm)].map(
      (m) => m[1]
    );
    expect(tableRows.length).toBeGreaterThan(0);
    for (const name of tableRows) expect(web).toContain(name);
  });

  it('names only fields the Go search code really answers', () => {
    const doc = repoFile('internal/search/document.go');
    const indexed = new Set([...doc.matchAll(/json:"([a-z_0-9]+)[",]/g)].map((m) => m[1]));
    const routing = repoFile('internal/search/field_routing.go');
    const aliasBlock = /var fieldAliases = map\[string\]string\{([^}]*)\}/.exec(routing)?.[1] ?? '';
    const aliases = new Set([...aliasBlock.matchAll(/"([a-z_]+)":/g)].map((m) => m[1]));
    const translator = repoFile('internal/search/bleve_translator.go');
    for (const f of QUERY_FIELD_HELP) {
      if (f.group === 'per_user') {
        expect(translator, `${f.name} per-user field`).toContain(`"${f.name}"`);
      } else if (f.name === 'duration') {
        expect(aliases.has('duration')).toBe(true);
        expect(indexed.has('duration_seconds')).toBe(true);
      } else {
        expect(indexed.has(f.name), `${f.name} must be an indexed field`).toBe(true);
      }
    }
  });

  it('shows rating and the iTunes fields only as sort options', () => {
    for (const f of ['rating', 'itunes_last_played', 'itunes_play_count']) {
      expect(names).not.toContain(f);
      expect(SORT_FIELDS.find((s) => s.value === f)?.sortOnly).toBe(true);
    }
    render(<Harness />);
    const help = screen.getByLabelText('Query help');
    expect(help.textContent).not.toMatch(/rating|itunes/i);
    // ...and the sort sources agree: the Go evaluator sorts on exactly these.
    const evaluator = repoFile('internal/playlist/evaluator.go');
    for (const s of SORT_FIELDS) expect(evaluator).toContain(`"${s.value}"`);
  });

  it('offers date_added and date_modified in both the help and the sort picker', () => {
    render(<Harness />);
    expect(
      within(screen.getByLabelText('Query help')).getByText(/date_added:>-1y/)
    ).toBeInTheDocument();
    expect(
      within(screen.getByLabelText('Query help')).getByText(/date_modified:>-30d/)
    ).toBeInTheDocument();
    expect(SORT_FIELDS.map((s) => s.value)).toEqual(
      expect.arrayContaining(['date_added', 'date_modified'])
    );
  });
});

describe('worked examples', () => {
  it('fill the box with exactly the strings the backend tests', () => {
    expect(WORKED_EXAMPLES.map((e) => e.query)).toEqual([
      'read_status:finished last_played:>-30d',
      'duration:>20h read_status:unstarted',
    ]);
    render(<Harness />);
    const buttons = screen.getAllByRole('button', { name: 'Use this query' });
    expect(buttons).toHaveLength(2);
    fireEvent.click(buttons[0]);
    expect(box().value).toBe('read_status:finished last_played:>-30d');
    fireEvent.click(buttons[1]);
    expect(box().value).toBe('duration:>20h read_status:unstarted');
    expect(
      screen.getByText(/Relative dates roll forward each time the collection refreshes/)
    ).toBeInTheDocument();
  });
});

describe('per-user note', () => {
  it.each(['read_status:finished', 'progress_pct:>50', 'last_played:>-30d'])(
    'shows for %s',
    (q) => {
      render(<Harness initial={q} />);
      const note = screen.getByText(new RegExp(PER_USER_NOTE.slice(0, 40)));
      expect(note.textContent).toContain(PER_USER_NOTE);
      expect(note.textContent).not.toContain('Creator');
      expect(note.closest('[role="status"]')).not.toBeNull();
    }
  );

  it('does not show for a book-only query', () => {
    render(<Harness initial="author:smith year:>=2015" />);
    expect(screen.queryByText(/reading state of whoever is viewing/)).toBeNull();
  });

  it('appears once a per-user field is typed', () => {
    render(<Harness />);
    expect(screen.queryByText(/reading state of whoever is viewing/)).toBeNull();
    fireEvent.change(box(), { target: { value: 'read_status:finished' } });
    expect(screen.getByText(/reading state of whoever is viewing/)).toBeInTheDocument();
    expect(usesPerUserField('-last_played:>-30d')).toBe(true);
    expect(usesPerUserField('author:smith')).toBe(false);
  });

  it('is fail-closed, like the server: a parse error or a field name in the text counts', () => {
    // Does not parse as a whole: cannot be shown to read no per-user state.
    expect(usesPerUserField('last_played:30d')).toBe(true);
    expect(usesPerUserField('title:/abc')).toBe(true);
    // Names a per-user field where the grammar reads something else.
    expect(usesPerUserField('author:read_status')).toBe(true);
    expect(usesPerUserField('"progress_pct:>5"')).toBe(true);
    expect(usesPerUserField('title:Hush last_played')).toBe(true);
    // Near misses stay book-only.
    expect(usesPerUserField('title:my_read_status_x')).toBe(false);
    expect(usesPerUserField('title:Hush OR author:kyle')).toBe(false);
  });
});

describe('validation and autocomplete', () => {
  it('shows a parse error under the field and clears it', () => {
    render(<Harness initial="last_played:30d" />);
    expect(screen.getByText(/needs a sign/)).toBeInTheDocument();
    expect(queryError('last_played:>-30d')).toBeNull();
    expect(queryError('title:/abc')).toMatch(/no closing/);
  });

  it('shows a server message verbatim when there is no parse error', () => {
    render(<QueryHelp value="author:x" onChange={vi.fn()} serverError="400 body text" />);
    expect(screen.getByText('400 body text')).toBeInTheDocument();
  });

  it('suggests field names for the token being typed and completes on click', () => {
    expect(suggestFields('author:smith rea')).toEqual(['read_status']);
    expect(suggestFields('author:smith ')).toEqual([]);
    expect(suggestFields('title:abc')).toEqual([]);
    render(<Harness initial="author:smith rea" />);
    fireEvent.click(screen.getByRole('button', { name: 'read_status' }));
    expect(box().value).toBe('author:smith read_status:');
  });
});

describe('sort picker', () => {
  it('labels sort-only fields and writes the sort_json shape', () => {
    const onChange = vi.fn();
    render(<SortPicker value={{ field: '', direction: 'asc' }} onChange={onChange} />);
    expect(screen.getByRole('option', { name: 'Rating (sort only)' })).toBeInTheDocument();
    expect(
      screen.getByRole('option', { name: 'iTunes play count (sort only)' })
    ).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Date added' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Sort by'), { target: { value: 'rating' } });
    expect(onChange).toHaveBeenCalledWith({ field: 'rating', direction: 'asc' });
  });

  it('round-trips sort_json', () => {
    expect(encodeSort({ field: 'year', direction: 'desc' })).toBe(
      '[{"field":"year","direction":"desc"}]'
    );
    expect(encodeSort({ field: '', direction: 'asc' })).toBe('');
    expect(decodeSort('[{"field":"year","direction":"desc"}]')).toEqual({
      field: 'year',
      direction: 'desc',
    });
    expect(decodeSort('not json')).toEqual({ field: '', direction: 'asc' });
    expect(decodeSort(undefined)).toEqual({ field: '', direction: 'asc' });
  });

  it('compares decoded choices and keeps directives past the first on write', () => {
    expect(
      sameSortChoice(
        { field: 'title', direction: 'asc' },
        decodeSort('[{"direction":"asc","field":"title"}]')
      )
    ).toBe(true);
    expect(sameSortChoice({ field: '', direction: 'asc' }, { field: '', direction: 'desc' })).toBe(
      true
    );
    expect(
      sameSortChoice({ field: 'title', direction: 'asc' }, { field: 'title', direction: 'desc' })
    ).toBe(false);
    const two = '[{"field":"title","direction":"asc"},{"field":"year","direction":"desc"}]';
    expect(applySortChoice(two, { field: 'rating', direction: 'desc' })).toBe(
      '[{"field":"rating","direction":"desc"},{"field":"year","direction":"desc"}]'
    );
    expect(applySortChoice(two, { field: '', direction: 'asc' })).toBe(
      '[{"field":"year","direction":"desc"}]'
    );
    expect(applySortChoice(undefined, { field: 'year', direction: 'asc' })).toBe(
      '[{"field":"year","direction":"asc"}]'
    );
    expect(
      applySortChoice('[{"field":"title","direction":"asc"}]', { field: '', direction: 'asc' })
    ).toBe('');
  });
});

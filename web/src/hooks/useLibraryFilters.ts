// file: web/src/hooks/useLibraryFilters.ts
// version: 1.7.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-12

import { useState, useEffect, useCallback } from 'react';
import { SortField, type FilterOptions } from '../types';
import * as api from '../services/api';

// parseSeriesIdParam reads the `series_id` URL param (TASK-167). Only a
// positive integer counts: the server parses the param with ParseQueryIntPtr,
// and an unparseable value there is treated as absent, which would list the
// whole library under a filter chip that says otherwise. Dropping it here
// keeps the chip and the query in agreement.
export function parseSeriesIdParam(raw: string | null): number | undefined {
  if (!raw || !/^\d+$/.test(raw)) return undefined;
  const n = Number(raw);
  return Number.isSafeInteger(n) && n > 0 ? n : undefined;
}

// defaultSortField is the Library sort when the URL names none. A series view
// (series_id set) defaults to series position: the book-detail series link
// lands there, and the library-wide title default scrambles a series' reading
// order. Every other view keeps title. Library.tsx uses this on BOTH sides of
// the URL round-trip -- to read an absent `sort` and to decide when writing
// one can be omitted -- so the two can never disagree.
export function defaultSortField(seriesId: number | undefined): SortField {
  return seriesId !== undefined ? SortField.SeriesPosition : SortField.Title;
}

// shallowEqualFilters compares two FilterOptions by value across the union of
// their keys. Non-URL fields (e.g. `tags`) are preserved by reference via the
// `...prev` spread in the sync effect, so a reference compare is correct for
// them. Used to keep a stable `filters` reference when a searchParams change
// (like page navigation) didn't actually change any filter value.
export function shallowEqualFilters(a: FilterOptions, b: FilterOptions): boolean {
  const keys = new Set<keyof FilterOptions>([
    ...(Object.keys(a) as (keyof FilterOptions)[]),
    ...(Object.keys(b) as (keyof FilterOptions)[]),
  ]);
  for (const k of keys) {
    if (a[k] !== b[k]) return false;
  }
  return true;
}

interface UseLibraryFiltersOptions {
  searchParams: URLSearchParams;
  /** Called when filters change so the caller can reset pagination to page 1. */
  onFiltersChange?: () => void;
}

export interface LibraryFiltersResult {
  filterOpen: boolean;
  setFilterOpen: (open: boolean) => void;
  filters: FilterOptions;
  setFilters: React.Dispatch<React.SetStateAction<FilterOptions>>;
  handleFiltersChange: (newFilters: FilterOptions) => void;
  selectedTags: string[];
  setSelectedTags: React.Dispatch<React.SetStateAction<string[]>>;
  handleTagFilterChange: (tags: string[]) => void;
  refreshTags: () => void;
  availableAuthors: string[];
  availableSeries: string[];
  /** Series id -> name, from the same getSeries() call that fills availableSeries. */
  seriesNameById: Map<number, string>;
  availableGenres: string[];
  availableLanguages: string[];
  availableTags: Array<{ tag: string; count: number }>;
  getActiveFilterCount: () => number;
}

export function useLibraryFilters({
  searchParams,
  onFiltersChange,
}: UseLibraryFiltersOptions): LibraryFiltersResult {
  const [filterOpen, setFilterOpen] = useState(false);
  const [filters, setFilters] = useState<FilterOptions>(() => ({
    author: searchParams.get('author') || undefined,
    series: searchParams.get('series') || undefined,
    genre: searchParams.get('genre') || undefined,
    language: searchParams.get('language') || undefined,
    libraryState: searchParams.get('state') || undefined,
    hasFileErrors: (searchParams.get('has_file_errors') === 'true') || undefined,
    fingerprintStatus: (searchParams.get('fingerprint_status') as "complete" | "partial" | "none" | null) || undefined,
    coveragePercentMin: searchParams.get('coverage_percent_min') ? parseInt(searchParams.get('coverage_percent_min')!, 10) : undefined,
    coveragePercentMax: searchParams.get('coverage_percent_max') ? parseInt(searchParams.get('coverage_percent_max')!, 10) : undefined,
    // Quick-filter preset params
    missingCovers: (searchParams.get('missing_covers') === 'true') || undefined,
    inImportPath: (searchParams.get('in_import_path') === 'true') || undefined,
    noIsbn: (searchParams.get('no_isbn') === 'true') || undefined,
    duplicatesFlagged: (searchParams.get('duplicates_flagged') === 'true') || undefined,
    versionGroupId: searchParams.get('version_group_id') || undefined,
    isPrimaryVersion: searchParams.get('is_primary_version') === 'false' ? false : undefined,
    seriesId: parseSeriesIdParam(searchParams.get('series_id')),
  }));
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [availableAuthors, setAvailableAuthors] = useState<string[]>([]);
  const [availableSeries, setAvailableSeries] = useState<string[]>([]);
  const [seriesNameById, setSeriesNameById] = useState<Map<number, string>>(() => new Map());
  const [availableGenres, setAvailableGenres] = useState<string[]>([]);
  const [availableLanguages, setAvailableLanguages] = useState<string[]>([]);
  const [availableTags, setAvailableTags] = useState<Array<{ tag: string; count: number }>>([]);

  useEffect(() => {
    api
      .listAllUserTags()
      .then((tags) => setAvailableTags(tags ?? []))
      .catch((_err) => {
        console.error('Failed to load tags:', _err);
      });
  }, []);

  useEffect(() => {
    api
      .getBookFacets()
      .then((facets) => {
        setAvailableGenres(facets.genres);
        setAvailableLanguages(facets.languages);
      })
      .catch((e) => {
        console.error('Failed to load facets:', e);
      });
  }, []);

  useEffect(() => {
    api
      .getAuthors()
      .then((authors) => {
        setAvailableAuthors(authors.map((a) => a.name).filter(Boolean).sort());
      })
      .catch((e) => {
        console.error('Failed to load authors:', e);
      });
  }, []);

  useEffect(() => {
    api
      .getSeries()
      .then((series) => {
        setAvailableSeries(series.map((s) => s.name).filter(Boolean).sort());
        setSeriesNameById(new Map(series.filter((s) => s.name).map((s) => [s.id, s.name])));
      })
      .catch((e) => {
        console.error('Failed to load series:', e);
      });
  }, []);

  const handleFiltersChange = useCallback(
    (newFilters: FilterOptions) => {
      setFilters(newFilters);
      onFiltersChange?.();
    },
    [onFiltersChange]
  );

  const handleTagFilterChange = useCallback((tags: string[]) => {
    setSelectedTags(tags);
    setFilters((prev) => ({ ...prev, tags: tags.length > 0 ? tags : undefined }));
  }, []);

  const refreshTags = useCallback(() => {
    api
      .listAllUserTags()
      .then((tags) => setAvailableTags(tags ?? []))
      .catch((_err) => {
        console.error('Failed to refresh tags:', _err);
      });
  }, []);

  // Sync filters whenever searchParams change (e.g., when navigating to /library with no params).
  //
  // This effect fires on EVERY searchParams change, including page-only navigation
  // (the URL carries ?page=N). Returning a brand-new object each time gave `filters`
  // a fresh reference on page changes, which re-triggered the page-reset effect in
  // Library.tsx (deps include `filters`) → the user got bounced back to page 1.
  // Fix: preserve prev's non-URL fields (tags, showFailed, …) and return the SAME
  // reference when nothing actually changed, so page navigation doesn't churn it.
  useEffect(() => {
    setFilters((prev) => {
      const next: FilterOptions = {
        ...prev,
        author: searchParams.get('author') || undefined,
        series: searchParams.get('series') || undefined,
        genre: searchParams.get('genre') || undefined,
        language: searchParams.get('language') || undefined,
        libraryState: searchParams.get('state') || undefined,
        hasFileErrors: (searchParams.get('has_file_errors') === 'true') || undefined,
        fingerprintStatus: (searchParams.get('fingerprint_status') as "complete" | "partial" | "none" | null) || undefined,
        coveragePercentMin: searchParams.get('coverage_percent_min') ? parseInt(searchParams.get('coverage_percent_min')!, 10) : undefined,
        coveragePercentMax: searchParams.get('coverage_percent_max') ? parseInt(searchParams.get('coverage_percent_max')!, 10) : undefined,
        missingCovers: (searchParams.get('missing_covers') === 'true') || undefined,
        inImportPath: (searchParams.get('in_import_path') === 'true') || undefined,
        noIsbn: (searchParams.get('no_isbn') === 'true') || undefined,
        duplicatesFlagged: (searchParams.get('duplicates_flagged') === 'true') || undefined,
        versionGroupId: searchParams.get('version_group_id') || undefined,
        isPrimaryVersion: searchParams.get('is_primary_version') === 'false' ? false : undefined,
        seriesId: parseSeriesIdParam(searchParams.get('series_id')),
      };
      return shallowEqualFilters(prev, next) ? prev : next;
    });
  }, [searchParams]);

  const getActiveFilterCount = useCallback(
    () => Object.values(filters).filter((v) => v !== undefined && v !== '').length,
    [filters]
  );

  return {
    filterOpen,
    setFilterOpen,
    filters,
    setFilters,
    handleFiltersChange,
    selectedTags,
    setSelectedTags,
    handleTagFilterChange,
    refreshTags,
    availableAuthors,
    availableSeries,
    seriesNameById,
    availableGenres,
    availableLanguages,
    availableTags,
    getActiveFilterCount,
  };
}

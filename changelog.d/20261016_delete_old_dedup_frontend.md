The frontend of the old Dedup page is deleted. `/dedup` and `/dedup/labels` have
redirected into Review since 03-PR10 shipped in v0.222.2, so the page, its nine
tab components, the Gold Labels page, their 71 unit tests and the two
`dedup*.spec.ts` Playwright files no longer have a route. Every deleted e2e test
has an equal or stricter counterpart in `review-authors-lane.spec.ts` or
`operation-monitoring.spec.ts`. The `api.ts` wrappers whose only callers were
those files (`getBookDuplicates`, `getBookDedupScanResults`, `scanBookDuplicates`,
`linkBookDuplicatesAsVersions`, `rejectBookDuplicateGroup`,
`queueBulkSplitBookMerge`) and their types are removed. The directory
`web/src/components/dedup/` is gone.

### Fixed

#### abs: Log warning when GetAllSeriesBookCounts() query fails

Added an slog.Warn call when GetAllSeriesBookCounts() fails in the LibrarySeries endpoint, with the message "abs: series book counts unavailable, reporting 0 for all series". This makes silent failures visible in logs while maintaining graceful degradation—the endpoint continues to serve a successful response with zero counts rather than failing the entire series list. A test (TestLibrarySeries_CountsErrorLogsWarning) verifies the warning logs on error and does not appear on the happy path.

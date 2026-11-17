- [ ] **WHISPER-ROUTE-UNREP** Route `path_unrepresentable` deliberately on the Go
      side — a Windows worker answers it for a book whose file name NTFS cannot
      hold (`:`, trailing dot, `CON`...), but `PathErrorCode` returns "" for an
      unknown code, so the book is just retried on whichever worker comes next.
      Teach `internal/whisper` to recognise the code, skip workers whose `/health`
      `path_flavor` is `nt` for that book, and record a non-durable status when no
      worker can take it. The safety rule on the worker is not to be loosened.

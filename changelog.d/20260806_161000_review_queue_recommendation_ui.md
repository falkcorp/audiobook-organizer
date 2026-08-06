<!-- file: changelog.d/20260806_161000_review_queue_recommendation_ui.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f2b6d40-3a71-4e58-b09c-1d4e8a5f7c62 -->
<!-- last-edited: 2026-08-06 -->

### Changed

- **The review queue now shows why each hold is there, and lets you disagree with it.**
  Every hold used to display the same sentence — literally the same one, on 762 of 777
  holds — which is a queue nobody can work. Each hold now shows the recommended action,
  the reason for it, and the numbers that reason is built from: how many files are in
  the group, how many of them have a known runtime, how many are long enough to be a
  book on their own, the median and longest runtime, and how many distinct titles are
  mixed together. The known-runtime count is highlighted when most are missing, because
  that gap is exactly why the system sometimes cannot decide.

  Each hold has its own action picker, starting on the recommendation. Holds the
  classifier could not call start on *nothing* and their Approve button stays disabled
  until a person chooses — no guess is pre-filled, least of all on the holds with the
  weakest evidence. "Not enough evidence" is shown as a state, never offered as a
  choice, because it is the machine's statement rather than a decision anyone can take.
  "Duplicate of an existing book" is offered but marked unimplemented, and the server's
  refusal is shown verbatim rather than dressed up as a generic failure.

  Approving a whole bucket still uses each hold's own recommendation, and the holds it
  skipped are now listed by id with their reason instead of vanishing behind a count —
  a bulk action that silently skips things is how someone comes to believe they cleared
  a queue they did not.

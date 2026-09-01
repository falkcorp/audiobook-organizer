### Fixed

- **Deciding a hold and then switching kind could paint the old kind's holds
  under the new kind's heading.** The regroup lane built its row request in two
  places — the mount fetch and the reload that follows every approve, reject and
  bulk action — and the second carried only a comment asserting it sent the same
  filter as the first. Reload deliberately has no abort signal (it must finish so
  the decided hold actually leaves the list), so a reviewer who changed kind
  while it was in flight got the previous kind's holds back, with no error and
  nothing on screen to say so. Both paths now share one request builder, and
  reload drops a response for a kind the lane is no longer showing.

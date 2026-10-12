- [ ] **AI-AUTHOR-REVIEW-GROUPS-EMPTY** The `ai.author-review` op's "groups"
      mode now runs against an empty group list when started generically (via
      the operations endpoints), because the deleted `ReviewDuplicateAuthors`
      handler was what computed `DedupGroups` and passed them in the op
      params. Move that computation into the op itself (`internal/server/ai_ops.go`,
      feeding `handlers.AIReviewGroupsMode`) so the mode works without a
      handler-supplied payload. Belongs to the 04 workstream.

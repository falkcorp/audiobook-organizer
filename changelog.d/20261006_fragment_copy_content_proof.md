### Changed

- **Fragment consolidation: a copy matched only by name and size is proven
  by its content (owner decision 2026-10-06, "hash both, read-only").** At
  plan time the fixer reads and hashes the claimant's file and the parent
  row's file (`filehash.BookFileHash`, the digest `file_hash` holds;
  streamed, no audio decoding) on a pool of four, honouring cancellation.
  Byte-identical content makes the claimant a proven copy: it lands in the
  applicable `copy:<parent>` row (and, into an iTunes-linked parent, retires
  writing the fragments only). Other bytes at the same size hold the
  claimant on its own `held:` row (`skipped_copy_unproven`, "content
  differs"); an unreadable file, or one over 100 MB where the file hash
  samples rather than reads every byte, leaves it `copy-unproven` with the
  reason. No file under the iTunes library is ever read. A hands-off
  claimant elsewhere (an iTunes id, Doctor Who / Big Finish) is compared
  too and stays on its manual-only row, never applicable, with the proof in
  its evidence for the owner ("list; I apply them").
- Read-only: no hash is stored on any book or row. The proof is kept in the
  plan row's evidence, state (each file's size and mtime as read) and
  fingerprint; re-plans, including Apply's under the merge lock, only
  re-stat the two files and report `changed_since_plan` when either moved.
- The proof is bound to each file's size, mtime, ctime, device and inode:
  a same-size rewrite with the mtime set back, or another file renamed over
  the path, is `changed_since_plan`. A claimant whose path is a hardlink or
  symlink to the parent's own file is the same file, not a copy: held
  ("same file as the parent row (path alias)"). The hash itself refuses a
  file over 100 MB, so a sampled digest can never become a proof.

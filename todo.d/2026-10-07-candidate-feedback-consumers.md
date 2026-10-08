- [ ] **CANDFB-1** Feed the `candfb:` candidate-feedback labels (Review →
      Candidates thumbs-down / applied positives) into
      `metafetch.calibrate-scoring` as a non-circular segment next to the
      manual-override one: a negative that outranked the applied candidate is
      a direct scoring miss. Also: the Candidates card keeps its thumbs-down
      marks per session only; hydrate them from `GET
      /metadata/candidate-feedback?book_id=` if the owner wants marks to
      survive a reload.

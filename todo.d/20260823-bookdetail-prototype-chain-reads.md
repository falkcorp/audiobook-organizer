- [ ] **BOOKDETAIL-PROTO-READ: three membership checks in `web/src/pages/BookDetail.tsx` read
      through the prototype chain, so a book id colliding with an `Object.prototype` member
      silently skips a fetch.** `if (!id || versionFileTags[id] !== undefined) return;` and the
      two sibling checks (`!versionSegments[versionId]`) resolve inherited members, so for
      `id ∈ {constructor, toString, valueOf, hasOwnProperty, __defineGetter__, ...}` the lookup
      returns an inherited function, which is `!== undefined`, and the preload is skipped.
      Currently invisible: `loadBook()` 404s on such an id and renders an error page before the
      skipped fetch could matter, so this is cosmetic today. Fix with `Object.hasOwn(map, id)`
      or by building these maps with `Object.create(null)`. Note this is the READ side — the
      three `js/remote-property-injection` alerts dismissed in #2798 were on the WRITE side
      (`[id]: value` in an object literal, which cannot pollute the prototype), and those
      dismissals are correct and unaffected. Found by review on #2798.

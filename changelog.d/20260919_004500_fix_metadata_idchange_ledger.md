- The bulk metadata apply no longer records author or series changes in the
  metadata history that never actually happened. It wrote the history entry at
  the moment it worked out the new author or series, before saving the book, so
  a save that failed — or a book deleted while the apply was running — left the
  history claiming a change you could not find anywhere in the data.
- Those history entries now also name the right old value. Because the apply
  looks up authors and series between reading a book and saving it, another job
  could change the book's author in that gap; the entry recorded the author the
  apply had read at the start rather than the one it actually replaced.

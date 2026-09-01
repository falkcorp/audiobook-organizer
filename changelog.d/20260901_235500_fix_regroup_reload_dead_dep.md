- Dropped a dependency from the regroup lane's `reload` callback that the refactor
  had already made dead. `reload` no longer builds its own request — `fetchPage`
  does, and it is keyed on the kind filter — so listing `kindFilter` a second time
  changed nothing about when `reload` is rebuilt and only told a reader the body
  reads something it does not.

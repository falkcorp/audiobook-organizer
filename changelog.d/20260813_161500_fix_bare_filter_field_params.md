### Fixed

- Asking the library to filter by a field name used directly in the web address
  — for example `?title=Skills` — silently listed the entire library instead,
  complete with a matching total, because field filters have to be sent in the
  `filters` parameter and anything else was ignored. Such a request is now
  answered with an error explaining the correct form, rather than with every
  book in the library. The web interface already used the correct form and was
  unaffected; this misled people querying the API by hand, including one
  investigation that recorded the wrong root cause because of it.

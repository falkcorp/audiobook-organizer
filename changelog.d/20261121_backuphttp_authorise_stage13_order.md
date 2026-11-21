### Fixed

- core backup routes: the handlers' own repeat of the route's permission and guards now asks stage 13's
  `httpapi.Authorize` instead of restating it, so a caller failing more than one leg is refused for the same leg
  the router refuses first (the interactive guard, then the owner guard, then the permission). It used to check the
  permission first while its comment said it followed stage 13's order. Every such refusal is a 403, as at HEAD;
  only the problem code could differ, and only for a handler mounted without the router (F4-08 follow-up).

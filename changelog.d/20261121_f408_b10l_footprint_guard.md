### Added

- Under core identity (`server.WithIdentity`, which nothing sets yet) the user footprint route (`GET /users/:id/footprint`) and `usermerge.Service.Footprint` answer 409 `merge_unavailable_under_identity` before any account is resolved or any store read, like the merge preview, apply and resume. The footprint reads books' own copy of an account's sign-ins and credentials, which hold nothing once the store is core's. The merge listing stays readable: it is accurate pre-flip history. A server without the option behaves exactly as before.

### Changed

#### The integration routes' sign-in and permission tests now run against the router's real authorization stage

The ten integration routes (adapters, list, get, health, create, update,
delete, the two test routes and the hold release) had no sign-in or
permission test, only placeholders waiting for the authorization stage. Each
now has a full table, run through the router's own stages 12 and 13 with the
real permission catalog: no sign-in is answered 401, a caller without the
route's permission (including one holding read alone on a write route) is
answered 403 naming the missing permission and leaves one denied-permission
audit row, and a holder gets the route's success with the audit rows the
spec requires (create, update, delete and test each record one action; reads
and a hold release record none). A refused request changes nothing, sends
nothing to the remote and never touches a hold. Tests only; no behaviour
change (F7-09 C1).

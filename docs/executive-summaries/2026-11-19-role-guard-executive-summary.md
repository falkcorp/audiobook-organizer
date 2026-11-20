<!-- file: docs/executive-summaries/2026-11-19-role-guard-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4867f7a3-90d2-48ed-881b-a49a520e46d7 -->
<!-- last-edited: 2026-11-19 -->

# Editing roles and users at the same moment can no longer leave the system with no administrator

**Date:** 19 November 2026 (rename to the merge date if it differs). Part of the
shared core's new sign-in and permissions code, which nothing in the running app
uses yet.

## Executive Summary

- The shared core has a rule that the last active administrator can never be
  removed. Until now the rule could be beaten by timing: if one person edited a
  role (for example, took the administrator permission away from a custom role)
  while another person changed a user at the very same moment, each change could
  look safe on its own and both could be saved, leaving nobody with
  administrator rights.
- The rule now watches the whole set of role definitions, not one role. Any
  change to any role, in the time between "the rule looked" and "the change was
  saved", makes the user change start over and look again.
- Deleting a role, and giving someone a role while that role is being deleted,
  can no longer both succeed. Before, a user could end up holding a role that no
  longer existed, which a later role with the same name would silently hand
  back.
- When many changes collide, a change tries again up to sixteen times with a
  short random pause. If it still cannot get through, the person sees a plain
  "busy, try again" answer, not a server error and not a "your copy is out of
  date" message.
- Nothing is switched on for users: the code that would let these collisions
  happen is not connected yet. The change closes the gap before that
  connection is made.

## A last-admin check that only watched one role

**What it was.** The check watched the single role that made the other
administrator an administrator. Two of its three exits (the user is not an
administrator now, or still is afterwards) decided on role definitions and
watched nothing.

**Why it mattered.** One person tidying their own roles while another edited a
custom administrator role could leave zero administrators, with nobody left who
could repair it from inside the app.

**The fix.** Every role change raises one counter. The check reads the counter
first and refuses to save if it moved, which covers every exit, roles that do not
exist, and a role deleted and re-created under the same name.

## Deleting a role that someone is just being given

**What it was.** The delete asked "does anyone hold this role?", got "no", and a
moment later a user was given it.

**Why it mattered.** The user ended up holding a role that does not exist, and
the delete skipped the administrator check that an edit already had.

**The fix.** Giving a role raises a second counter, and a delete watches it
before it looks at who holds the role. The delete also runs the same last-admin
check an edit does.

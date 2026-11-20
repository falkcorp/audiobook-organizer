<!-- file: docs/executive-summaries/2026-11-19-role-guard-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 4867f7a3-90d2-48ed-881b-a49a520e46d7 -->
<!-- last-edited: 2026-11-19 -->

# Editing roles and users at the same moment can no longer leave the system with no administrator

**Date:** 19 November 2026. Part of the shared core's new sign-in and
permissions code, which nothing in the running app uses yet.

**Pull request:** [#4771](https://github.com/falkcorp/audiobook-organizer/pull/4771) (merge commit: pending).

**Related specs:** [05-rbac](../specs/platform-core/05-rbac.md) (the
last-admin rule and who may give which role) and
[05-authn](../specs/platform-core/05-authn.md).

## Executive Summary

- The shared core has a rule that the last active administrator can never be
  removed. Until now the rule could be beaten by timing: if one person edited a
  role (for example, took the administrator permission away from a custom role)
  while another person changed a user at the very same moment, each change could
  look safe on its own and both could be saved, leaving nobody with
  administrator rights.
- The rule now watches the whole set of role definitions. Before this change it
  watched no role definition at all, only the other administrator's user record.
  Any change to any role, in the time between "the rule looked" and "the change
  was saved", now makes the user change start over and look again.
- Deleting a role while someone is being given it can no longer both succeed.
  Before, a user could end up holding a role that no longer existed, which a
  later role with the same name would silently hand back. Giving a role now also
  refuses a role that does not exist when the person giving it is an
  administrator; before, administrators were let through on a name nobody had
  defined.
- When many changes collide, a change tries again, and if it still cannot get
  through the person sees a plain "busy, try again" answer, not a server error
  and not a "your copy is out of date" message. Changing a role and giving a
  role try up to sixteen times with a short random pause. Deactivating or
  deleting a user keeps its four tries, and merging two accounts is retried by
  whoever asked for it.
- Nothing is switched on for users: the code that would let these collisions
  happen is not connected yet. The change closes the gap before that
  connection is made.
- Verified by tests that force each ordering of the colliding changes, both ways
  round, and by 68 deliberately broken variants of the new logic, every one of
  which a test catches.

## The last-admin check that watched no role

**What it was.** The check looked at who else was an administrator and made sure
that other person's account was unchanged when the change was saved. It never
checked that the roles making them an administrator were unchanged. Whether a
user is an administrator, whether they still will be, and which other
administrator is left were all decided from role definitions that nothing
watched.

**Why it mattered.** One person tidying their own roles while another edited a
custom administrator role could leave zero administrators, with nobody left who
could repair it from inside the app.

**The fix.** Every role change raises one counter. The check reads the counter
first and refuses to save if it moved, which covers every case the check can
end in, roles that do not exist, and a role deleted and re-created under the
same name.

## Deleting a role that someone is just being given

**What it was.** The delete asked "does anyone hold this role?", got "no", and a
moment later a user was given it. The delete also skipped the administrator
check that an edit already had. Separately, the check on "may this person give
this role?" waved administrators through when the role did not exist.

**Why it mattered.** The user ended up holding a role that does not exist, and
a role created later under the same name would grant them whatever it carried.
That includes a pending invite to a role that is deleted before the invitee
accepts it.

**The fix.** Giving a role raises a second counter, and a delete watches it
before it looks at who holds the role. The delete also runs the same last-admin
check an edit does. Giving a role is now checked against the stored role, and a
role that does not exist is refused for everyone, administrators included. An
invite to a role that has since gone is refused and used up, like any other
refused invite. A failed read of the stored role is reported as a server
problem, not as a refusal, so a storage fault neither tells the person they
lack a permission nor uses up an invite link.

## Busy, try again instead of an error

**What it was.** When two changes collided, the loser either gave up quickly
with a raw storage error or, for role changes, reported "your copy is out of
date" even though its copy was current.

**Why it mattered.** A person who did nothing wrong saw a server error or was
told to reload, and retrying by hand was the only way forward.

**The fix.** Changes that give or edit roles now try up to sixteen times with a
short random pause between tries, which is enough for a burst of fifteen other
changes to land first. Past that the person gets a "busy, try again" answer that
says how long to wait. Other user changes keep their existing number of tries.

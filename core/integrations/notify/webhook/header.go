// file: core/integrations/notify/webhook/header.go
// version: 1.0.0
// guid: 7c4e1a96-3b58-4d02-9f17-a0e6d5b82c13
// last-edited: 2026-11-13

package webhook

// Header is one custom request header of an instance. The adapter's own
// headers (X-Mediacore-Delivery, X-Mediacore-Signature, Authorization from
// the token, Content-Type) can never be shadowed by one: a name that is
// reserved fails the settings schema, so a Put, a Test or an import that names
// one is refused as invalid settings (05-notify section 3.1).
//
// The name is a header token (letters, digits and !#$%&'*+.^_|~-) that is not,
// in any letter case, "Authorization", "Content-Type" or anything starting
// "X-Mediacore-". JSON Schema has no "not" over a pattern that every client
// engine reads the same way, and a map's keys cannot carry a pattern at all,
// so the rule is spelled as the complement of those names: at every position
// the name either leaves the reserved spelling for good, or is a proper prefix
// of it, or (for the two whole-name words) goes on past it. TestHeaderNamePattern
// holds this expression to Reserved over every spelling of the three names and
// their neighbours; the expression is generated, never edited by hand.
type Header struct {
	Name  string `json:"name" schema:"pattern=^(?:[._|'#$*!%~&+0-9BD-WYZbd-wyz^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Aa](?:[._|'#$*!%~&+0-9A-TV-Za-tv-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Uu](?:[._|'#$*!%~&+0-9A-SU-Za-su-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Tt](?:[._|'#$*!%~&+0-9A-GI-Za-gi-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Hh](?:[._|'#$*!%~&+0-9A-NP-Za-np-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Oo](?:[._|'#$*!%~&+0-9A-QS-Za-qs-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Rr](?:[._|'#$*!%~&+0-9A-HJ-Za-hj-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Ii](?:[._|'#$*!%~&+0-9A-Ya-y^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Zz](?:[._|'#$*!%~&+0-9B-Zb-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Aa](?:[._|'#$*!%~&+0-9A-SU-Za-su-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Tt](?:[._|'#$*!%~&+0-9A-HJ-Za-hj-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Ii](?:[._|'#$*!%~&+0-9A-NP-Za-np-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Oo](?:[._|'#$*!%~&+0-9A-MO-Za-mo-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Nn](?:[._|'#$*!%~&+0-9A-Za-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[._|'#$*!%~&+0-9A-Za-z^-]+)|)|)|)|)|)|)|)|)|)|)|)|)|[Cc](?:[._|'#$*!%~&+0-9A-NP-Za-np-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Oo](?:[._|'#$*!%~&+0-9A-MO-Za-mo-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Nn](?:[._|'#$*!%~&+0-9A-SU-Za-su-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Tt](?:[._|'#$*!%~&+0-9A-DF-Za-df-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Ee](?:[._|'#$*!%~&+0-9A-MO-Za-mo-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Nn](?:[._|'#$*!%~&+0-9A-SU-Za-su-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Tt](?:[._|'#$*!%~&+0-9A-Za-z^][._|'#$*!%~&+0-9A-Za-z^-]*|-(?:[._|'#$*!%~&+0-9A-SU-Za-su-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Tt](?:[._|'#$*!%~&+0-9A-XZa-xz^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Yy](?:[._|'#$*!%~&+0-9A-OQ-Za-oq-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Pp](?:[._|'#$*!%~&+0-9A-DF-Za-df-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Ee](?:[._|'#$*!%~&+0-9A-Za-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[._|'#$*!%~&+0-9A-Za-z^-]+)|)|)|)|)|)|)|)|)|)|)|)|[Xx](?:[._|'#$*!%~&+0-9A-Za-z^][._|'#$*!%~&+0-9A-Za-z^-]*|-(?:[._|'#$*!%~&+0-9A-LN-Za-ln-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Mm](?:[._|'#$*!%~&+0-9A-DF-Za-df-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Ee](?:[._|'#$*!%~&+0-9A-CE-Za-ce-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Dd](?:[._|'#$*!%~&+0-9A-HJ-Za-hj-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Ii](?:[._|'#$*!%~&+0-9B-Zb-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Aa](?:[._|'#$*!%~&+0-9ABD-Zabd-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Cc](?:[._|'#$*!%~&+0-9A-NP-Za-np-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Oo](?:[._|'#$*!%~&+0-9A-QS-Za-qs-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Rr](?:[._|'#$*!%~&+0-9A-DF-Za-df-z^-][._|'#$*!%~&+0-9A-Za-z^-]*|[Ee](?:[._|'#$*!%~&+0-9A-Za-z^][._|'#$*!%~&+0-9A-Za-z^-]*|)|)|)|)|)|)|)|)|)|)|))$"`
	Value string `json:"value"`
}

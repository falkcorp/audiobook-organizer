// file: internal/server/handlers/phone_app_steps.go
// version: 1.0.0
// guid: dc8db512-0961-4e2c-a747-9c5014eb7267
// last-edited: 2026-11-07

// The phone-app steps shared by two emails, and the email that carries a
// guest's phone-app key.
//
//   - The invitation email (invite_delivery.go) says where to put a key that
//     does not exist yet: the invitation page shows it once, after accept.
//   - The key email (RenderPhoneKeyEmail) carries the key itself: an admin issued
//     or reissued it, and the server mails it to the guest's verified address.
//
// Both include the "phoneAppSteps" partial (AudioBooth on iPhone, Lissen on
// Android). KeyInEmail says which wording to use: false points at "the two
// values from the invitation page", true at "the two headers below" and adds
// the line for a guest whose app still holds the old key. The partials live in
// package handlers because server imports handlers and not the reverse, and a
// new package would need its own entry in internal/arch/layering_test.go.
//
// Android names Lissen, not the official Audiobookshelf app, on purpose; see
// the note at the top of invite_delivery.go.

package handlers

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"net/url"
	"strings"
	texttemplate "text/template"

	imail "github.com/falkcorp/audiobook-organizer/internal/mail"
)

// phoneAppStepsTextDef defines the "phoneAppSteps" text partial. Its data needs
// Username, ServerURL and KeyInEmail. With KeyInEmail false the output is
// exactly the steps the invitation email has always carried.
const phoneAppStepsTextDef = `{{define "phoneAppSteps"}}iPhone or iPad: install AudioBooth from the App Store.
  1. Open AudioBooth, go to Servers and tap + to add a server.
  2. Server address: {{.ServerURL}}
  3. Before signing in, tap Custom Headers, then Add Header. Add each of the two
     {{if .KeyInEmail}}headers below{{else}}values from the invitation page{{end}} under the name shown next to it.
  4. Go back and sign in with your username ({{.Username}}) and password.

Android 9 or newer: install Lissen from Google Play or F-Droid.
  https://play.google.com/store/apps/details?id=org.grakovne.lissen
  (The official Audiobookshelf Android app cannot connect to this server.)
  1. Open Lissen. On the "Connect to server" screen, tap the gear icon.
  2. Tap Connection, then Custom Headers. Add the two {{if .KeyInEmail}}headers below{{else}}values from the invitation
     page{{end}}: Key is the name shown, Value is the value next to it.
  3. Go back. Server URL: {{.ServerURL}}  Login: {{.Username}}
     Password: the one you chose. Tap Connect.{{if .KeyInEmail}}

If the app is already set up with your old key, open the same Custom Headers screen
and replace both values with the two above.{{end}}{{end}}`

// phoneAppStepsHTMLDef is the HTML twin of phoneAppStepsTextDef.
const phoneAppStepsHTMLDef = `{{define "phoneAppSteps"}}<p><strong>iPhone or iPad:</strong> install AudioBooth from the App Store.</p>
<ol>
<li>Open AudioBooth, go to <strong>Servers</strong> and tap <strong>+</strong> to add a server.</li>
<li>Server address: <code>{{.ServerURL}}</code></li>
<li>Before signing in, tap <strong>Custom Headers</strong>, then <strong>Add Header</strong>. Add each of the two {{if .KeyInEmail}}headers below{{else}}values from the invitation page{{end}} under the name shown next to it.</li>
<li>Go back and sign in with your username (<strong>{{.Username}}</strong>) and password.</li>
</ol>
<p><strong>Android 9 or newer:</strong> install <a href="https://play.google.com/store/apps/details?id=org.grakovne.lissen">Lissen from Google Play</a> or F-Droid. The official Audiobookshelf Android app cannot connect to this server.</p>
<ol>
<li>Open Lissen. On the <strong>Connect to server</strong> screen, tap the gear icon.</li>
<li>Tap <strong>Connection</strong>, then <strong>Custom Headers</strong>. Add the two {{if .KeyInEmail}}headers below{{else}}values from the invitation page{{end}}: <strong>Key</strong> is the name shown, <strong>Value</strong> is the value next to it.</li>
<li>Go back. Server URL: <code>{{.ServerURL}}</code>, Login: <strong>{{.Username}}</strong>, Password: the one you chose. Tap <strong>Connect</strong>.</li>
</ol>
{{if .KeyInEmail}}<p>If the app is already set up with your old key, open the same Custom Headers screen and replace both values with the two above.</p>
{{end}}{{end}}`

// phoneAppStepsData is what the partial reads.
type phoneAppStepsData struct {
	Username   string
	ServerURL  string
	KeyInEmail bool
}

// Why an email cannot go out; the reasons EmailSendBlocked returns. The
// invitation wording (mailNotConfiguredReason and publicURLMissingReason in
// invite_delivery.go) adds the "copy the link instead" advice, which makes no
// sense for a key email.
const (
	mailNotConfiguredCore = "email is not configured on this server (SMTP_HOST and SMTP_FROM)"
	publicURLMissingCore  = "the server's public address is not set (PUBLIC_BASE_URL or EXTERNAL_URL)"
)

// EmailSendBlocked returns why an email that names the server's address cannot
// be sent right now, or "" when it can: mail must be configured (sender is
// non-nil) and base must be an absolute http(s) URL, since a relative link in
// an email opens nothing. The invitation email and the phone-app key email
// share this rule.
func EmailSendBlocked(sender imail.Sender, base string) string {
	if sender == nil {
		return mailNotConfiguredCore
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return publicURLMissingCore
	}
	return ""
}

// PhoneKeyEmail is the input to RenderPhoneKeyEmail. It holds the client
// secret, so every fmt verb prints it redacted (the same rule as the server's
// guestTokenIssue).
type PhoneKeyEmail struct {
	Username     string
	ServerURL    string
	ClientID     string
	ClientSecret string
	// Reissue is true when the guest already had a key that this one replaces
	// (their old key no longer works); false for a first key.
	Reissue bool
}

func (p PhoneKeyEmail) redacted() string {
	return fmt.Sprintf("PhoneKeyEmail{Reissue:%v ClientID:[redacted] ClientSecret:[redacted]}", p.Reissue)
}
func (p PhoneKeyEmail) String() string   { return p.redacted() }
func (p PhoneKeyEmail) GoString() string { return p.redacted() }
func (p PhoneKeyEmail) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(p.redacted()))
}

// phoneKeyEmailData is the template's view: a flat struct with no formatting
// methods, plus KeyInEmail for the shared partial (always true here).
type phoneKeyEmailData struct {
	Username     string
	ServerURL    string
	ClientID     string
	ClientSecret string
	Reissue      bool
	KeyInEmail   bool
}

const (
	phoneKeySubjectFirst   = "Your Audiobook Organizer phone-app key"
	phoneKeySubjectReissue = "Your new Audiobook Organizer phone-app key"
)

var phoneKeyTextTmpl = texttemplate.Must(texttemplate.New("text").Parse(`Hello {{.Username}},

{{if .Reissue}}An administrator issued you a new phone-app key. Your old key no longer works.{{else}}Here is your own phone-app key.{{end}}

Your phone app (AudioBooth or Lissen) must send these two custom headers to {{.ServerURL}}
on every request:

  CF-Access-Client-Id: {{.ClientID}}
  CF-Access-Client-Secret: {{.ClientSecret}}

{{template "phoneAppSteps" .}}

This key belongs to you alone. Do not share or forward it. Delete this email once the app works.

{{if .Reissue}}If you did not ask for a new key{{else}}If you did not expect this key{{end}}, tell the person who runs this server.
` + phoneAppStepsTextDef))

var phoneKeyHTMLTmpl = htmltemplate.Must(htmltemplate.New("html").Parse(`<!doctype html>
<html><body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;line-height:1.5;color:#1d1d1f;max-width:600px">
<p>Hello {{.Username}},</p>
<p>{{if .Reissue}}An administrator issued you a new phone-app key. Your old key no longer works.{{else}}Here is your own phone-app key.{{end}}</p>
<p>Your phone app (AudioBooth or Lissen) must send these two custom headers to <a href="{{.ServerURL}}">{{.ServerURL}}</a> on every request:</p>
<p><code>CF-Access-Client-Id: {{.ClientID}}</code><br>
<code>CF-Access-Client-Secret: {{.ClientSecret}}</code></p>
{{template "phoneAppSteps" .}}
<p><strong>This key belongs to you alone. Do not share or forward it. Delete this email once the app works.</strong></p>
<p style="color:#666">{{if .Reissue}}If you did not ask for a new key{{else}}If you did not expect this key{{end}}, tell the person who runs this server.</p>
</body></html>` + phoneAppStepsHTMLDef))

// RenderPhoneKeyEmail builds the email that carries a guest's phone-app key.
// The caller sets To (the guest's verified address). Templates escape every
// value: html/template contextually, and the text part has no markup. The
// rendered body holds the secret, so callers must not log it or put it in a
// response.
func RenderPhoneKeyEmail(in PhoneKeyEmail) (imail.Message, error) {
	data := phoneKeyEmailData{
		Username: in.Username, ServerURL: in.ServerURL, ClientID: in.ClientID,
		ClientSecret: in.ClientSecret, Reissue: in.Reissue, KeyInEmail: true,
	}
	var text, html bytes.Buffer
	if err := phoneKeyTextTmpl.Execute(&text, data); err != nil {
		return imail.Message{}, err
	}
	if err := phoneKeyHTMLTmpl.Execute(&html, data); err != nil {
		return imail.Message{}, err
	}
	subject := phoneKeySubjectFirst
	if in.Reissue {
		subject = phoneKeySubjectReissue
	}
	return imail.Message{Subject: subject, Text: text.String(), HTML: html.String()}, nil
}

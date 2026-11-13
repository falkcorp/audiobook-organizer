// file: internal/server/handlers/phone_app_steps_test.go
// version: 1.0.3
// guid: 17a9d2d3-c383-4119-8b22-80a2d492f512
// last-edited: 2026-11-13

package handlers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/coretest"
)

const (
	testKeySecret = "SENTINEL-client-secret-0123456789abcdef"
	testKeyID     = "abcdef0123456789.access"
)

func TestRenderPhoneKeyEmail_CarriesBothHeadersAndTheSteps(t *testing.T) {
	for _, reissue := range []bool{true, false} {
		msg, err := RenderPhoneKeyEmail(PhoneKeyEmail{
			Username: "bruce.wayne", ServerURL: "https://books.example.com",
			ClientID: testKeyID, ClientSecret: testKeySecret, Reissue: reissue,
		})
		if err != nil {
			t.Fatal(err)
		}
		if msg.To != "" {
			t.Fatalf("To = %q: the caller sets it", msg.To)
		}
		wantSubject := "Your Audiobook Organizer phone-app key"
		if reissue {
			wantSubject = "Your new Audiobook Organizer phone-app key"
		}
		if msg.Subject != wantSubject {
			t.Fatalf("reissue=%v subject = %q", reissue, msg.Subject)
		}
		for _, part := range []string{msg.Text, msg.HTML} {
			for _, want := range []string{
				"CF-Access-Client-Id: " + testKeyID, "CF-Access-Client-Secret: " + testKeySecret,
				"https://books.example.com", "AudioBooth", "Lissen", "org.grakovne.lissen", "Custom Headers",
				"headers below", "Delete this email once the app works",
			} {
				if !strings.Contains(part, want) {
					t.Fatalf("reissue=%v: missing %q in %s", reissue, want, part)
				}
			}
			if strings.Contains(part, "invitation page") {
				t.Fatalf("reissue=%v: the key email points at the invitation page: %s", reissue, part)
			}
			if got := strings.Contains(part, "Your old key no longer works"); got != reissue {
				t.Fatalf("reissue=%v: old-key line present = %v", reissue, got)
			}
		}
	}
}

func TestRenderPhoneKeyEmail_HTMLEscapesValues(t *testing.T) {
	msg, err := RenderPhoneKeyEmail(PhoneKeyEmail{
		Username: "joker<script>alert(1)</script>", ServerURL: "https://books.example.com",
		ClientID: "id<b>", ClientSecret: testKeySecret + "<i>", Reissue: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"<script>", "id<b>", "<i>"} {
		if strings.Contains(msg.HTML, raw) {
			t.Fatalf("HTML carries %q unescaped: %s", raw, msg.HTML)
		}
	}
	if !strings.Contains(msg.HTML, "&lt;script&gt;") {
		t.Fatalf("HTML did not escape the username: %s", msg.HTML)
	}
}

func TestPhoneKeyEmail_NeverPrintsTheSecret(t *testing.T) {
	p := PhoneKeyEmail{ClientID: testKeyID, ClientSecret: testKeySecret}
	for _, s := range []string{fmt.Sprint(p), fmt.Sprintf("%+v", p), fmt.Sprintf("%#v", p), fmt.Sprintf("%v", &p)} {
		if strings.Contains(s, testKeySecret) || strings.Contains(s, testKeyID) {
			t.Fatalf("credential printed: %s", s)
		}
	}
}

// The invitation keeps pointing at the invitation page, and the shared partial
// stays the same text the invitation email has always carried.
func TestInviteEmail_StepsPointAtTheInvitationPage(t *testing.T) {
	inv := &database.Invite{Username: "robin", Email: "robin@example.com", ExpiresAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	msg, err := renderInviteEmail(inv, "https://books.example.com/invite/abc", "https://books.example.com", false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{msg.Text, msg.HTML} {
		for _, want := range []string{"values from the invitation", "Custom Headers", "Lissen"} {
			if !strings.Contains(part, want) {
				t.Fatalf("invitation missing %q: %s", want, part)
			}
		}
		for _, banned := range []string{"headers below", "replace both values", "CF-Access-Client"} {
			if strings.Contains(part, banned) {
				t.Fatalf("invitation carries key-email wording %q: %s", banned, part)
			}
		}
	}
}

func TestEmailSendBlocked(t *testing.T) {
	rec := &coretest.MailRecorder{}
	for _, tt := range []struct {
		name   string
		sender *coretest.MailRecorder
		base   string
		want   string
	}{
		{"ok", rec, "https://books.example.com", ""},
		{"ok with a trailing slash", rec, "https://books.example.com/", ""},
		{"ok over http", rec, "http://books.lan:8484", ""},
		{"no mail", nil, "https://books.example.com", mailNotConfiguredCore},
		{"empty base", rec, "", publicURLMissingCore},
		{"relative base", rec, "/books", publicURLMissingCore},
		{"scheme-less host", rec, "books.example.com", publicURLMissingCore},
		{"other scheme", rec, "ftp://books.example.com", publicURLMissingCore},
	} {
		// A nil *Recorder in the interface would not be a nil Sender, so pass
		// the interface value the production caller passes.
		var got string
		if tt.sender == nil {
			got = EmailSendBlocked(nil, tt.base)
		} else {
			got = EmailSendBlocked(tt.sender, tt.base)
		}
		if got != tt.want {
			t.Errorf("%s: EmailSendBlocked = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// The "already set up with your old key" line is for a guest who HAS an old key.
func TestRenderPhoneKeyEmail_OldKeyLineIsForReissueOnly(t *testing.T) {
	const line = "already set up with your old key"
	for _, reissue := range []bool{true, false} {
		msg, err := RenderPhoneKeyEmail(PhoneKeyEmail{
			Username: "bruce.wayne", ServerURL: "https://books.example.com",
			ClientID: testKeyID, ClientSecret: testKeySecret, Reissue: reissue,
		})
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"text": msg.Text, "html": msg.HTML} {
			if got := strings.Contains(body, line); got != reissue {
				t.Errorf("reissue=%v %s part contains the old-key line = %v", reissue, name, got)
			}
		}
	}
}

// A base URL with a trailing slash must not leak a double slash into the email.
func TestRenderPhoneKeyEmail_TrimsTheServerURLSlash(t *testing.T) {
	msg, err := RenderPhoneKeyEmail(PhoneKeyEmail{
		Username: "bruce.wayne", ServerURL: "https://books.example.com/",
		ClientID: testKeyID, ClientSecret: testKeySecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"text": msg.Text, "html": msg.HTML} {
		if strings.Contains(body, "example.com/") {
			t.Errorf("%s part keeps the trailing slash: %s", name, body)
		}
		if !strings.Contains(body, "https://books.example.com") {
			t.Errorf("%s part lost the server address", name)
		}
	}
}

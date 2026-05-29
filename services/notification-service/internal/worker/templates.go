package worker

import (
	"bytes"
	"fmt"
	"html/template"
	plaintemplate "text/template"
	"time"
)

// TemplateData is the rendering input for an email template.
type TemplateData struct {
	ResetLink  string
	AcceptLink string
	ExpiresAt  time.Time
}

const ( //nolint:gosec // G101: template strings contain the word "password" as display text, not as a credential value
	passwordResetSubject = "Reset your NChat password" //nolint:gosec

	passwordResetTextTmpl = `` + //nolint:gosec
		"Reset your NChat password by visiting: {{.ResetLink}}\nThis link expires at {{.ExpiresAt}}."

	passwordResetHTMLTmpl = `<html><body><p>Reset your NChat password by visiting: <a href="{{.ResetLink}}">{{.ResetLink}}</a></p><p>This link expires at {{.ExpiresAt}}.</p></body></html>` //nolint:gosec

	inviteSubject  = "You've been invited to NChat"
	inviteTextTmpl = `You've been invited to NChat. Accept at: {{.AcceptLink}}
This link expires at {{.ExpiresAt}}.`
	inviteHTMLTmpl = `<html><body><p>You've been invited to NChat. Accept at: <a href="{{.AcceptLink}}">{{.AcceptLink}}</a></p><p>This link expires at {{.ExpiresAt}}.</p></body></html>`
)

// RenderTemplate renders the email subject, text body, and HTML body for the given kind.
func RenderTemplate(kind string, data TemplateData) (subject, textBody, htmlBody string, err error) {
	switch kind {
	case "password_reset":
		subject = passwordResetSubject
		textBody, err = renderText(passwordResetTextTmpl, data)
		if err != nil {
			return
		}
		htmlBody, err = renderHTML(passwordResetHTMLTmpl, data)
	case "invite":
		subject = inviteSubject
		textBody, err = renderText(inviteTextTmpl, data)
		if err != nil {
			return
		}
		htmlBody, err = renderHTML(inviteHTMLTmpl, data)
	default:
		err = fmt.Errorf("unknown email template kind: %q", kind)
	}
	return
}

func renderText(tmplStr string, data TemplateData) (string, error) {
	t, err := plaintemplate.New("").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse text template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute text template: %w", err)
	}
	return buf.String(), nil
}

func renderHTML(tmplStr string, data TemplateData) (string, error) {
	t, err := template.New("").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse html template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute html template: %w", err)
	}
	return buf.String(), nil
}

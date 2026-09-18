package urlsafety

import "testing"

func TestClassifyURL(t *testing.T) {
	cases := map[string]URLClass{
		"https://example.com/":                                           URLClassPublic,
		"https://example.com/docs/auth/overview":                         URLClassPublic,
		"https://shop.example.com/?code=BR10":                            URLClassPublic,
		"https://www.youtube.com/watch?v=abc":                            URLClassPublic,
		"https://example.com/blog/why-we-invite-feedback":                URLClassPublic,
		"https://app.example.com/login?otp=123456":                       URLClassSensitive,
		"https://app.example.com/login?TOKEN=abc":                        URLClassSensitive,
		"https://app.example.com/reset-password/abc":                     URLClassSensitive,
		"https://app.example.com/magic-link?x=1":                         URLClassSensitive,
		"https://app.example.com/oauth/callback?code=abc&state=xyz":      URLClassSensitive,
		"https://app.example.com/cb?code=abc&state=xyz":                  URLClassSensitive,
		"https://bucket.s3.amazonaws.com/f.pdf?X-Amz-Signature=deadbeef": URLClassSensitive,
		"https://acct.blob.core.windows.net/c/b?sig=abc%3D":              URLClassSensitive,
		"https://storage.googleapis.com/b/o?X-Goog-Signature=abc":        URLClassSensitive,
		"https://team.example.com/invite/abc":                            URLClassSensitive,
		"https://app.example.com/?verification%5Fcode=abc":               URLClassSensitive,
		"https://wiki.internal/page":                                     URLClassInternal,
		"https://printer.local/":                                         URLClassInternal,
		"https://intranet.corp/":                                         URLClassInternal,
		"https://host.home.arpa/":                                        URLClassInternal,
		"https://example.test/":                                          URLClassPublic,
		"https://intranet.example.com.internal/page?jwt=abc":             URLClassInternal,
		"http://%zz": URLClassPublic,
	}
	for raw, want := range cases {
		if got := ClassifyURL(raw); got != want {
			t.Errorf("%s: want %s, got %s", raw, want, got)
		}
	}
}

func TestIsInternalHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "intranet": true, "": true,
		"wiki.internal.": true, "example.com": false, "sub.example.com": false,
	} {
		if got := IsInternalHost(host); got != want {
			t.Errorf("%q: want %v, got %v", host, want, got)
		}
	}
}

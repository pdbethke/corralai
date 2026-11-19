// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestDesktopBrowserURLIsLocalLoopbackNeverCarriesToken is the regression
// guard for the bearer-token-in-URL leak: the old launchApp pointed the
// browser straight at the daemon with `?token=...` appended, which put the
// bearer into browser history / referrer / daemon access logs. desktop must
// instead always point the browser at the LOCAL console host's loopback
// address, and the URL must never carry a token in any form. Written to FAIL
// against the old launchApp behavior (which appended "?token=" + token).
func TestDesktopBrowserURLIsLocalLoopbackNeverCarriesToken(t *testing.T) {
	const sampleToken = "super-secret-bearer-token"
	got := desktopBrowserURL("127.0.0.1:54321")

	if got != "http://127.0.0.1:54321" {
		t.Errorf("desktopBrowserURL(%q) = %q, want %q", "127.0.0.1:54321", got, "http://127.0.0.1:54321")
	}
	if !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("desktopBrowserURL result %q must start with http://127.0.0.1: (loopback only)", got)
	}
	if strings.Contains(got, "token") {
		t.Errorf("desktopBrowserURL result %q must never contain %q", got, "token")
	}
	if strings.Contains(got, sampleToken) {
		t.Errorf("desktopBrowserURL result %q must never contain a bearer token value", got)
	}
}

// setupFixture builds the first-run setup server with a recording save and
// done, so a test can see whether a request reached either.
func setupFixture(t *testing.T) (http.Handler, string, *[]Config, *int) {
	t.Helper()
	var saved []Config
	launched := 0
	cfg := Config{}
	h, token, err := newSetupServer(&cfg,
		func(c Config) error { saved = append(saved, c); return nil },
		func(Config) { launched++ })
	if err != nil {
		t.Fatal(err)
	}
	return h, token, &saved, &launched
}

func setupPost(token, brain, origin, host string) *http.Request {
	form := url.Values{"brain_url": {brain}, "token": {"tok"}}
	if token != "" {
		form.Set("setup_token", token)
	}
	r := httptest.NewRequest(http.MethodPost, "http://"+host+"/save", strings.NewReader(form.Encode()))
	r.Host = host
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

// The setup page is the only thing allowed to save a config: it embeds a
// per-launch token, and the save it submits is accepted.
func TestSetupPageSavesWhatItsOwnFormSubmits(t *testing.T) {
	h, token, saved, _ := setupFixture(t)
	page := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5000/", nil)
	h.ServeHTTP(page, get)
	if !strings.Contains(page.Body.String(), `name="setup_token" value="`+token+`"`) {
		t.Fatalf("the setup form does not carry its per-launch token:\n%s", page.Body.String())
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, setupPost(token, "https://brain.example.test/", "http://127.0.0.1:5000", "127.0.0.1:5000"))
	if w.Code != http.StatusOK || len(*saved) != 1 || (*saved)[0].BrainURL != "https://brain.example.test" {
		t.Fatalf("own-form save refused: code=%d saved=%v body=%s", w.Code, *saved, w.Body.String())
	}
}

// Any web page the operator has open can make their browser POST a form to
// a loopback port. Without the token, from another origin, or through a
// DNS-rebound host name, a save must be refused before anything is written.
func TestSetupRefusesASaveThatDidNotComeFromItsOwnPage(t *testing.T) {
	cases := map[string]func(token string) *http.Request{
		"no token": func(string) *http.Request {
			return setupPost("", "https://evil.example.test", "http://127.0.0.1:5000", "127.0.0.1:5000")
		},
		"wrong token": func(string) *http.Request {
			return setupPost("not-the-token", "https://evil.example.test", "http://127.0.0.1:5000", "127.0.0.1:5000")
		},
		"cross-site origin": func(tok string) *http.Request {
			return setupPost(tok, "https://evil.example.test", "https://evil.example.test", "127.0.0.1:5000")
		},
		"no origin or referer": func(tok string) *http.Request {
			return setupPost(tok, "https://evil.example.test", "", "127.0.0.1:5000")
		},
		"rebound host": func(tok string) *http.Request {
			return setupPost(tok, "https://evil.example.test", "http://evil.example.test:5000", "evil.example.test:5000")
		},
		"GET": func(tok string) *http.Request {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5000/save?setup_token="+tok+"&brain_url=https://evil.example.test", nil)
			r.Header.Set("Origin", "http://127.0.0.1:5000")
			return r
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			h, token, saved, _ := setupFixture(t)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req(token))
			if w.Code < 400 || len(*saved) != 0 {
				t.Fatalf("save admitted: code=%d saved=%v", w.Code, *saved)
			}
		})
	}
}

// A rebound host must not be able to read the page either: it carries the
// token, and any saved bearer, in its form.
func TestSetupPageRefusesARebindingHost(t *testing.T) {
	h, _, _, _ := setupFixture(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://evil.example.test:5000/", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("setup page served to a non-loopback Host: code=%d", w.Code)
	}
}

// The brain address must be an http(s) URL with a host; anything else is
// refused, never saved.
func TestSetupRefusesABrainThatIsNotAnHTTPURL(t *testing.T) {
	for _, brain := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "http://", "ftp://brain.example.test", "brain.example.test", "https://user:pw@brain.example.test"} {
		t.Run(brain, func(t *testing.T) {
			h, token, saved, _ := setupFixture(t)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, setupPost(token, brain, "http://127.0.0.1:5000", "127.0.0.1:5000"))
			if w.Code != http.StatusBadRequest || len(*saved) != 0 {
				t.Fatalf("brain %q: code=%d saved=%v", brain, w.Code, *saved)
			}
		})
	}
}

// /done launches the app; a page elsewhere must not be able to trigger it.
func TestSetupDoneNeedsTheToken(t *testing.T) {
	h, token, _, launched := setupFixture(t)
	for _, tok := range []string{"", "not-the-token"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5000/done?setup_token="+tok, nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("/done with token %q: code=%d", tok, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5000/done?setup_token="+token, nil))
	if w.Code != http.StatusOK || *launched != 1 {
		t.Fatalf("/done with the token: code=%d launched=%d", w.Code, *launched)
	}
}

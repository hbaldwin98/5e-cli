package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCodexLoginUsesBrowserPKCEAndFormExchange(t *testing.T) {
	jwt := testJWT(t, "acct-123")
	var tokenForm url.Values
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", got)
		}
		var err error
		tokenForm, err = url.ParseQuery(readBody(t, r))
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "refresh_token": "refresh", "id_token": jwt, "expires_in": 3600,
		})
	}))
	defer tokenServer.Close()

	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	usedListener := false
	var browserChallenge string
	fixedNow := time.Unix(1_700_000_000, 0)
	config := CodexOAuthConfig{
		AuthorizeURL: "https://auth.example/oauth/authorize",
		TokenURL:     tokenServer.URL,
		Random:       strings.NewReader(strings.Repeat("r", 96)),
		Now:          func() time.Time { return fixedNow },
		Listen: func(_, _ string) (net.Listener, error) {
			usedListener = true
			return listener, nil
		},
	}
	config.OpenBrowser = func(rawURL string) error {
		authURL, err := url.Parse(rawURL)
		if err != nil {
			return err
		}
		q := authURL.Query()
		browserChallenge = q.Get("code_challenge")
		for key, want := range map[string]string{
			"client_id": CodexClientID, "response_type": "code", "scope": codexLoginScope,
			"code_challenge_method": "S256", "id_token_add_organizations": "true",
			"codex_cli_simplified_flow": "true", "originator": "codex_cli_rs",
		} {
			if got := q.Get(key); got != want {
				t.Errorf("authorize %s = %q, want %q", key, got, want)
			}
		}
		go func() {
			_, _ = http.Get(q.Get("redirect_uri") + "?code=auth-code&state=" + url.QueryEscape(q.Get("state")))
		}()
		return nil
	}

	cred, err := config.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !usedListener {
		t.Fatal("configured listener was not used")
	}
	if cred.Type != OAuthAuth || cred.AccessToken != "access" || cred.RefreshToken != "refresh" || cred.AccountID != "acct-123" {
		t.Fatalf("unexpected credential: %+v", cred)
	}
	if cred.ExpiresAt != fixedNow.Add(time.Hour).Unix() {
		t.Fatalf("ExpiresAt = %d", cred.ExpiresAt)
	}
	if tokenForm.Get("grant_type") != "authorization_code" || tokenForm.Get("code") != "auth-code" {
		t.Fatalf("unexpected token form: %v", tokenForm)
	}
	verifier := tokenForm.Get("code_verifier")
	challengeBytes := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	if browserChallenge != wantChallenge {
		t.Fatalf("browser PKCE challenge = %q, want %q", browserChallenge, wantChallenge)
	}
}

func TestCodexRefreshUsesJSONAndPreservesRotatableFields(t *testing.T) {
	jwt := testJWT(t, "acct-new")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"client_id": CodexClientID, "grant_type": "refresh_token",
			"refresh_token": "old-refresh", "scope": codexRefreshScope,
		} {
			if got := body[key]; got != want {
				t.Errorf("refresh %s = %q, want %q", key, got, want)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access", "id_token": jwt, "expires_in": 60,
		})
	}))
	defer server.Close()

	config := CodexOAuthConfig{TokenURL: server.URL, Now: func() time.Time { return time.Unix(100, 0) }}
	updated, err := config.Refresh(context.Background(), Credential{
		Type: OAuthAuth, RefreshToken: "old-refresh", AccountID: "acct-old",
		ChatModel: "chat", EmbedModel: "embed", BaseURL: "base",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != "new-access" || updated.RefreshToken != "old-refresh" || updated.AccountID != "acct-new" {
		t.Fatalf("unexpected refreshed credential: %+v", updated)
	}
	if updated.ChatModel != "chat" || updated.EmbedModel != "embed" || updated.BaseURL != "base" || updated.ExpiresAt != 160 {
		t.Fatalf("refresh did not preserve metadata: %+v", updated)
	}
}

func TestCodexLoginRejectsWrongStateBeforeExchange(t *testing.T) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	config := CodexOAuthConfig{
		Random: strings.NewReader(strings.Repeat("x", 96)),
		Listen: func(_, _ string) (net.Listener, error) { return listener, nil },
		OpenBrowser: func(rawURL string) error {
			u, _ := url.Parse(rawURL)
			go func() { _, _ = http.Get(u.Query().Get("redirect_uri") + "?code=nope&state=wrong") }()
			return nil
		},
	}
	if _, err := config.Login(context.Background()); err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("want state mismatch, got %v", err)
	}
}

func TestCodexAccessTokenReturnsCurrentStoredToken(t *testing.T) {
	path := t.TempDir() + "/auth.json"
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Set(Codex, Credential{
		Type: OAuthAuth, AccessToken: "current", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	token, accountID, err := CodexAccessToken(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "current" || accountID != "acct" {
		t.Fatalf("token, account = %q, %q", token, accountID)
	}
}

func testJWT(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestCodexBeginRedeemExchangesPastedRedirect(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","expires_in":3600}`)
	}))
	defer server.Close()

	config := CodexOAuthConfig{TokenURL: server.URL, HTTPClient: server.Client()}
	request, err := config.Begin(config.PasteRedirectURI())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !strings.Contains(request.AuthorizeURL, "code_challenge_method=S256") {
		t.Fatalf("authorize URL missing PKCE challenge: %s", request.AuthorizeURL)
	}
	if request.RedirectURI != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect URI = %q", request.RedirectURI)
	}

	pasted := "http://localhost:1455/auth/callback?code=abc123&state=" + request.State
	cred, err := request.Redeem(context.Background(), pasted)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if cred.AccessToken != "at" || cred.Type != OAuthAuth {
		t.Fatalf("credential = %+v", cred)
	}
	if gotForm.Get("code") != "abc123" {
		t.Fatalf("exchanged code = %q", gotForm.Get("code"))
	}
	if gotForm.Get("code_verifier") == "" {
		t.Fatal("exchange omitted code_verifier")
	}
	if gotForm.Get("redirect_uri") != request.RedirectURI {
		t.Fatalf("redirect_uri = %q", gotForm.Get("redirect_uri"))
	}
}

func TestCodexRedeemRejectsMismatchedState(t *testing.T) {
	config := CodexOAuthConfig{TokenURL: "http://127.0.0.1:1/unused"}
	request, err := config.Begin(config.PasteRedirectURI())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = request.Redeem(context.Background(), "http://localhost:1455/auth/callback?code=abc&state=someone-elses")
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("err = %v, want a state mismatch", err)
	}
}

func TestParseCallbackInputAcceptsWhatIsEasyToCopy(t *testing.T) {
	for _, tc := range []struct {
		name, input, code, state string
	}{
		{"full URL", "http://localhost:1455/auth/callback?code=a1&state=s1", "a1", "s1"},
		{"query only", "code=a2&state=s2", "a2", "s2"},
		{"bare code", "a3", "a3", ""},
		{"surrounding space", "  http://localhost:1455/auth/callback?code=a4  ", "a4", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, state, err := parseCallbackInput(tc.input)
			if err != nil {
				t.Fatalf("parseCallbackInput: %v", err)
			}
			if code != tc.code || state != tc.state {
				t.Fatalf("got (%q, %q), want (%q, %q)", code, state, tc.code, tc.state)
			}
		})
	}
}

func TestParseCallbackInputSurfacesAuthorizationErrors(t *testing.T) {
	_, _, err := parseCallbackInput("http://localhost:1455/auth/callback?error=access_denied&error_description=User+refused")
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want the authorization error", err)
	}
	if _, _, err := parseCallbackInput("http://localhost:1455/auth/callback?state=s"); err == nil {
		t.Fatal("want an error when the pasted URL carries no code")
	}
}

// A headless caller sets OnAuthorizeURL and no OpenBrowser; Login must still
// reach the authorization URL rather than refusing for want of a browser.
func TestCodexLoginAnnouncesURLWithoutABrowser(t *testing.T) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	announced := make(chan string, 1)
	config := CodexOAuthConfig{
		TokenURL: "http://127.0.0.1:1/unused",
		Listen:   func(_, _ string) (net.Listener, error) { return listener, nil },
		OnAuthorizeURL: func(rawURL string) {
			announced <- rawURL
			// Deliver the callback the way a browser opened elsewhere would.
			parsed, _ := url.Parse(rawURL)
			redirect := parsed.Query().Get("redirect_uri")
			go func() {
				resp, err := http.Get(redirect + "?code=pasted&state=" + parsed.Query().Get("state"))
				if err == nil {
					resp.Body.Close()
				}
			}()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The exchange fails against the unroutable token URL, which is fine: the
	// point is that Login got as far as announcing and accepting the callback.
	_, err = config.Login(ctx)
	if err == nil || !strings.Contains(err.Error(), "exchange code") {
		t.Fatalf("err = %v, want a token-exchange failure", err)
	}
	select {
	case rawURL := <-announced:
		if !strings.Contains(rawURL, "code_challenge=") {
			t.Fatalf("announced URL = %s", rawURL)
		}
	default:
		t.Fatal("Login never announced the authorization URL")
	}
}

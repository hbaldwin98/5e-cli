package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	CodexIssuer       = "https://auth.openai.com"
	CodexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexCallbackPath = "/auth/callback"
	CodexCallbackPort = 1455
	CodexBaseURL      = "https://chatgpt.com/backend-api/codex"

	codexLoginScope   = "openid profile email offline_access"
	codexRefreshScope = "openid profile email"
)

// CodexOAuthConfig contains the Codex OAuth endpoints and replaceable I/O used
// by the browser flow. Zero values use the Codex CLI defaults.
type CodexOAuthConfig struct {
	Issuer        string
	ClientID      string
	AuthorizeURL  string
	TokenURL      string
	CallbackPath  string
	ListenAddress string
	HTTPClient    *http.Client
	Listen        func(network, address string) (net.Listener, error)
	OpenBrowser   func(url string) error
	Random        io.Reader
	Now           func() time.Time
}

// CodexAccessToken returns a current Codex token and account ID, refreshing
// and persisting the credential shortly before it expires.
func CodexAccessToken(ctx context.Context, storePath string, client *http.Client) (string, string, error) {
	store, err := Load(storePath)
	if err != nil {
		return "", "", err
	}
	cred, ok := store.Get(Codex)
	if !ok || cred.Type != OAuthAuth {
		return "", "", fmt.Errorf("codex is not authenticated; run `5e auth login codex`")
	}
	if cred.AccessToken != "" && cred.ExpiresAt > time.Now().Add(time.Minute).Unix() {
		return cred.AccessToken, cred.AccountID, nil
	}
	cfg := DefaultCodexOAuthConfig()
	cfg.HTTPClient = client
	updated, err := cfg.Refresh(ctx, cred)
	if err != nil {
		return "", "", err
	}
	store.Set(Codex, updated)
	if err := store.Save(); err != nil {
		return "", "", fmt.Errorf("save refreshed codex credential: %w", err)
	}
	return updated.AccessToken, updated.AccountID, nil
}

// DefaultCodexOAuthConfig returns the OpenAI Codex browser OAuth settings.
func DefaultCodexOAuthConfig() CodexOAuthConfig {
	return CodexOAuthConfig{
		Issuer:        CodexIssuer,
		ClientID:      CodexClientID,
		CallbackPath:  CodexCallbackPath,
		ListenAddress: fmt.Sprintf("localhost:%d", CodexCallbackPort),
	}
}

// Login opens the Codex authorization page, waits for the loopback callback,
// exchanges its code using PKCE S256, and returns a credential ready to save.
func (c CodexOAuthConfig) Login(ctx context.Context) (Credential, error) {
	c = c.withDefaults()
	if c.OpenBrowser == nil {
		return Credential{}, fmt.Errorf("codex oauth: OpenBrowser is required")
	}

	listener, err := c.Listen("tcp", c.ListenAddress)
	if err != nil {
		return Credential{}, fmt.Errorf("codex oauth: listen: %w", err)
	}
	defer listener.Close()

	redirectURI, err := callbackURL(listener, c.CallbackPath)
	if err != nil {
		return Credential{}, fmt.Errorf("codex oauth: callback URL: %w", err)
	}
	state, err := randomURLString(c.Random, 32)
	if err != nil {
		return Credential{}, fmt.Errorf("codex oauth: create state: %w", err)
	}
	verifier, err := randomURLString(c.Random, 64)
	if err != nil {
		return Credential{}, fmt.Errorf("codex oauth: create PKCE verifier: %w", err)
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	authorizeURL, err := url.Parse(c.AuthorizeURL)
	if err != nil {
		return Credential{}, fmt.Errorf("codex oauth: parse authorize URL: %w", err)
	}
	query := authorizeURL.Query()
	query.Set("client_id", c.ClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", codexLoginScope)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", "codex_cli_rs")
	authorizeURL.RawQuery = query.Encode()

	type callbackResult struct {
		code string
		err  error
	}
	result := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(c.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			select {
			case result <- callbackResult{err: fmt.Errorf("codex oauth: callback state mismatch")}:
			default:
			}
			return
		}
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			description := r.URL.Query().Get("error_description")
			http.Error(w, "Authorization failed", http.StatusBadRequest)
			select {
			case result <- callbackResult{err: fmt.Errorf("codex oauth: authorization failed: %s: %s", oauthErr, description)}:
			default:
			}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			select {
			case result <- callbackResult{err: fmt.Errorf("codex oauth: callback omitted authorization code")}:
			default:
			}
			return
		}
		_, _ = io.WriteString(w, "Authorization complete. You can close this window.\n")
		select {
		case result <- callbackResult{code: code}:
		default:
		}
	})
	server := &http.Server{Handler: mux}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer server.Close()

	if err := c.OpenBrowser(authorizeURL.String()); err != nil {
		return Credential{}, fmt.Errorf("codex oauth: open browser: %w", err)
	}

	select {
	case got := <-result:
		if got.err != nil {
			return Credential{}, got.err
		}
		return c.exchangeCode(ctx, got.code, verifier, redirectURI)
	case err := <-serveDone:
		if err == nil || err == http.ErrServerClosed {
			err = fmt.Errorf("callback server stopped")
		}
		return Credential{}, fmt.Errorf("codex oauth: %w", err)
	case <-ctx.Done():
		return Credential{}, fmt.Errorf("codex oauth: %w", ctx.Err())
	}
}

// Refresh exchanges cred.RefreshToken and returns an updated credential. The
// caller can persist it with Store.Set(Codex, updated) followed by Store.Save.
func (c CodexOAuthConfig) Refresh(ctx context.Context, cred Credential) (Credential, error) {
	c = c.withDefaults()
	if cred.RefreshToken == "" {
		return Credential{}, fmt.Errorf("codex oauth: credential has no refresh token")
	}
	body, err := json.Marshal(map[string]string{
		"client_id":     c.ClientID,
		"grant_type":    "refresh_token",
		"refresh_token": cred.RefreshToken,
		"scope":         codexRefreshScope,
	})
	if err != nil {
		return Credential{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(string(body)))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	tokens, err := c.doTokenRequest(req)
	if err != nil {
		return Credential{}, fmt.Errorf("codex oauth: refresh token: %w", err)
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = cred.RefreshToken
	}
	if tokens.IDToken == "" {
		tokens.IDToken = cred.IDToken
	}
	updated, err := c.credentialFromTokens(tokens)
	if err != nil {
		return Credential{}, err
	}
	if updated.AccountID == "" {
		updated.AccountID = cred.AccountID
	}
	updated.ChatModel = cred.ChatModel
	updated.EmbedModel = cred.EmbedModel
	updated.BaseURL = cred.BaseURL
	return updated, nil
}

type codexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c CodexOAuthConfig) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (Credential, error) {
	form := url.Values{
		"client_id":     {c.ClientID},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Credential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokens, err := c.doTokenRequest(req)
	if err != nil {
		return Credential{}, fmt.Errorf("codex oauth: exchange code: %w", err)
	}
	return c.credentialFromTokens(tokens)
}

func (c CodexOAuthConfig) doTokenRequest(req *http.Request) (codexTokenResponse, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return codexTokenResponse{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return codexTokenResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codexTokenResponse{}, fmt.Errorf("%s: %s", resp.Status, errorBodyMessage(payload, resp.Status))
	}
	var tokens codexTokenResponse
	if err := json.Unmarshal(payload, &tokens); err != nil {
		return codexTokenResponse{}, fmt.Errorf("parse response: %w", err)
	}
	if tokens.AccessToken == "" {
		return codexTokenResponse{}, fmt.Errorf("response omitted access_token")
	}
	return tokens, nil
}

func (c CodexOAuthConfig) credentialFromTokens(tokens codexTokenResponse) (Credential, error) {
	accountID := ""
	if tokens.IDToken != "" {
		var err error
		accountID, err = codexAccountID(tokens.IDToken)
		if err != nil {
			return Credential{}, fmt.Errorf("codex oauth: parse ID token: %w", err)
		}
	}
	return Credential{
		Type:         OAuthAuth,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		AccountID:    accountID,
		ExpiresAt:    c.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).Unix(),
	}, nil
}

func (c CodexOAuthConfig) withDefaults() CodexOAuthConfig {
	defaults := DefaultCodexOAuthConfig()
	if c.Issuer == "" {
		c.Issuer = defaults.Issuer
	}
	if c.ClientID == "" {
		c.ClientID = defaults.ClientID
	}
	if c.AuthorizeURL == "" {
		c.AuthorizeURL = strings.TrimRight(c.Issuer, "/") + "/oauth/authorize"
	}
	if c.TokenURL == "" {
		c.TokenURL = strings.TrimRight(c.Issuer, "/") + "/oauth/token"
	}
	if c.CallbackPath == "" {
		c.CallbackPath = defaults.CallbackPath
	}
	if c.ListenAddress == "" {
		c.ListenAddress = defaults.ListenAddress
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	if c.Listen == nil {
		c.Listen = net.Listen
	}
	if c.Random == nil {
		c.Random = rand.Reader
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

func callbackURL(listener net.Listener, path string) (string, error) {
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", err
	}
	return "http://localhost:" + port + path, nil
}

func randomURLString(source io.Reader, size int) (string, error) {
	b := make([]byte, size)
	if _, err := io.ReadFull(source, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func codexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode claims: %w", err)
	}
	return claims.Auth.AccountID, nil
}

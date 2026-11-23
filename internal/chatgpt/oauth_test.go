package chatgpt

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type testTransport func(*http.Request) (*http.Response, error)

func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func jsonResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data)))}
}

func signedIDToken(key *rsa.PrivateKey, clientID, nonce string) string {
	claims, _ := json.Marshal(map[string]any{
		"iss": issuer, "aud": clientID, "sub": "account-subject", "email": "beaver@example.com",
		"nonce": nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	encode := base64.RawURLEncoding.EncodeToString
	payload := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claims)
	digest := sha256.Sum256([]byte(payload))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return payload + "." + encode(signature)
}

func TestChatGPTLoginVerifiesStateAndUsesIssuedClient(t *testing.T) {
	for _, wrongNonce := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrongNonce=%t", wrongNonce), func(t *testing.T) {
			client := New(t.TempDir())
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			client.keys = &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}
			const issuedID = "oaiapp_beaver_test"
			var authorizeQuery atomic.Pointer[url.Values]
			client.http.Transport = testTransport(func(request *http.Request) (*http.Response, error) {
				query := *authorizeQuery.Load()
				if err := request.ParseForm(); err != nil {
					return nil, err
				}
				form := request.PostForm
				digest := sha256.Sum256([]byte(form.Get("code_verifier")))
				if request.URL.String() != tokenURL || form.Get("client_id") != issuedID ||
					form.Get("code") != "auth-code" || form.Get("grant_type") != "authorization_code" ||
					form.Get("resource") != APIBaseURL || form.Get("redirect_uri") != query.Get("redirect_uri") ||
					base64.RawURLEncoding.EncodeToString(digest[:]) != query.Get("code_challenge") {
					return nil, fmt.Errorf("invalid token exchange")
				}
				nonce := query.Get("nonce")
				if wrongNonce {
					nonce = "unexpected"
				}
				return jsonResponse(http.StatusOK, map[string]any{
					"access_token": "access-test", "refresh_token": "refresh-test", "token_type": "Bearer",
					"id_token": signedIDToken(key, issuedID, nonce), "expires_in": 3600, "scope": scopes,
				}), nil
			})
			login, err := client.StartLogin(t.Context(), "", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer login.Cancel()
			authorize, err := url.Parse(login.URL)
			if err != nil {
				t.Fatal(err)
			}
			query := authorize.Query()
			authorizeQuery.Store(&query)
			if authorize.Scheme+"://"+authorize.Host+authorize.Path != authorizeURL ||
				query.Get("client_id") != dynamicClientID || query.Get("resource") != APIBaseURL ||
				query.Get("code_challenge_method") != "S256" || !hasScope(query.Get("scope"), planScope) ||
				!strings.HasPrefix(query.Get("redirect_uri"), "http://127.0.0.1:") {
				t.Fatalf("invalid authorization request: %s", login.URL)
			}

			callback := query.Get("redirect_uri")
			browser := &http.Client{Timeout: 5 * time.Second}
			invalid, err := browser.Get(callback + "?state=invalid&code=ignored")
			if err != nil {
				t.Fatal(err)
			}
			invalid.Body.Close()
			if invalid.StatusCode != http.StatusBadRequest {
				t.Fatalf("invalid state returned %d", invalid.StatusCode)
			}
			valid, err := browser.Get(callback + "?" + url.Values{
				"state": {query.Get("state")}, "code": {"auth-code"}, "client_id": {issuedID},
			}.Encode())
			if err != nil {
				t.Fatal(err)
			}
			valid.Body.Close()
			account, err := login.Result()
			if wrongNonce {
				if err == nil {
					t.Fatal("accepted an ID token with the wrong nonce")
				}
				if _, err := client.load(accountID(issuedID)); err != ErrAccountMissing {
					t.Fatalf("invalid login persisted credentials: %v", err)
				}
				return
			}
			if err != nil || account.ID != accountID(issuedID) || !account.PlanAuthorized || account.Email != "beaver@example.com" {
				t.Fatalf("login failed: %+v, %v", account, err)
			}
			public, _ := json.Marshal(account)
			if strings.Contains(string(public), "access-test") || strings.Contains(string(public), issuedID) {
				t.Fatal("account metadata exposed OAuth credentials")
			}
			if info, err := os.Stat(client.path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
				t.Fatalf("credential file must be private: %v, %v", info, err)
			}
		})
	}
}

func TestChatGPTRefreshSerializesRotationAndSignOut(t *testing.T) {
	client := New(t.TempDir())
	old := credential{
		ClientID: "oaiapp_old", Subject: "test",
		AccessToken: "old-access", RefreshToken: "old-refresh", Scopes: scopes, ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := client.save(old); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	client.http.Transport = testTransport(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			return nil, err
		}
		form := request.PostForm
		if form.Get("client_id") != old.ClientID {
			return nil, fmt.Errorf("invalid client ID")
		}
		switch request.URL.String() {
		case tokenURL:
			refreshes.Add(1)
			if form.Get("refresh_token") != old.RefreshToken || form.Get("scope") != "" || form.Get("resource") != APIBaseURL {
				return nil, fmt.Errorf("invalid refresh")
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"access_token": "new-access", "refresh_token": "new-refresh", "token_type": "Bearer", "expires_in": 3600,
			}), nil
		case revocationURL:
			if form.Get("token") != "new-refresh" || form.Get("token_type_hint") != "refresh_token" {
				return nil, fmt.Errorf("revocation did not use the rotated token")
			}
			return jsonResponse(http.StatusOK, nil), nil
		default:
			return nil, fmt.Errorf("unexpected authorization endpoint")
		}
	})
	var calls sync.WaitGroup
	for range 5 {
		calls.Go(func() {
			token, err := client.accessToken(t.Context(), accountID(old.ClientID))
			if err != nil || token != "new-access" {
				t.Errorf("refresh failed: %v", err)
			}
		})
	}
	calls.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refreshed %d times", refreshes.Load())
	}
	revoked, err := client.SignOut(t.Context(), accountID(old.ClientID))
	if err != nil || !revoked {
		t.Fatalf("sign out failed: %v, revoked=%v", err, revoked)
	}
	value, err := client.load(accountID(old.ClientID))
	if err != nil || value.AccessToken != "" || value.RefreshToken != "" || value.account().PlanAuthorized ||
		value.ClientID != old.ClientID || value.Subject != old.Subject {
		t.Fatalf("sign-out must preserve the registration and remove tokens: %v", err)
	}
}

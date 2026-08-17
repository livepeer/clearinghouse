package auth0verify

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const (
	testIssuer   = "https://idp.test"
	testAudience = "livepeer-clearinghouse"
	testClientID = "public-client-abc"
	testSubject  = "auth0|user-1"
)

func testKeyPair(t *testing.T) (jwk.Key, jwk.Set) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	key, err := jwk.FromRaw(priv)
	if err != nil {
		t.Fatalf("jwk from raw: %v", err)
	}
	if err := key.Set(jwk.KeyIDKey, "test-kid"); err != nil {
		t.Fatalf("set kid: %v", err)
	}
	if err := key.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		t.Fatalf("set alg: %v", err)
	}
	pub, err := key.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		t.Fatalf("add key: %v", err)
	}
	return key, set
}

func signToken(t *testing.T, key jwk.Key, issuer, aud, azp, sub string, extra map[string]any) string {
	t.Helper()
	builder := jwt.NewBuilder().
		Issuer(issuer).
		Audience([]string{aud}).
		Subject(sub).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(5 * time.Minute))
	if azp != "" {
		builder = builder.Claim("azp", azp)
	}
	for k, v := range extra {
		builder = builder.Claim(k, v)
	}
	tok, err := builder.Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, key))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return string(signed)
}

func TestVerifyUserAccessToken_Valid(t *testing.T) {
	key, set := testKeyPair(t)
	v := NewWithKeySet(testIssuer, testAudience, set)
	raw := signToken(t, key, testIssuer+"/", testAudience, testClientID, testSubject, nil)

	clientID, userID, err := v.VerifyUserAccessToken(context.Background(), raw, testClientID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if clientID != testClientID || userID != testSubject {
		t.Fatalf("got client=%q user=%q", clientID, userID)
	}
}

func TestVerifyUserAccessToken_TrailingSlashIssuer(t *testing.T) {
	key, set := testKeyPair(t)
	v := NewWithKeySet(testIssuer+"/", testAudience, set)
	raw := signToken(t, key, testIssuer, testAudience, testClientID, testSubject, nil)

	if _, _, err := v.VerifyUserAccessToken(context.Background(), raw, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyUserAccessToken_WrongAudience(t *testing.T) {
	key, set := testKeyPair(t)
	v := NewWithKeySet(testIssuer, testAudience, set)
	raw := signToken(t, key, testIssuer, "other-aud", testClientID, testSubject, nil)

	if _, _, err := v.VerifyUserAccessToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected audience error")
	}
}

func TestVerifyUserAccessToken_WrongIssuer(t *testing.T) {
	key, set := testKeyPair(t)
	v := NewWithKeySet(testIssuer, testAudience, set)
	raw := signToken(t, key, "https://other.example", testAudience, testClientID, testSubject, nil)

	if _, _, err := v.VerifyUserAccessToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected issuer error")
	}
}

func TestVerifyUserAccessToken_ClientMismatch(t *testing.T) {
	key, set := testKeyPair(t)
	v := NewWithKeySet(testIssuer, testAudience, set)
	raw := signToken(t, key, testIssuer, testAudience, testClientID, testSubject, nil)

	if _, _, err := v.VerifyUserAccessToken(context.Background(), raw, "other-client"); err == nil {
		t.Fatal("expected client mismatch")
	}
}

func TestVerifyUserAccessToken_MissingClaims(t *testing.T) {
	key, set := testKeyPair(t)
	v := NewWithKeySet(testIssuer, testAudience, set)
	raw := signToken(t, key, testIssuer, testAudience, "", "", nil)

	if _, _, err := v.VerifyUserAccessToken(context.Background(), raw, ""); err == nil {
		t.Fatal("expected missing claims error")
	}
}

func TestVerifyUserAccessToken_ActionClaimFallbacks(t *testing.T) {
	key, set := testKeyPair(t)
	v := NewWithKeySet(testIssuer, testAudience, set)
	raw := signToken(t, key, testIssuer, testAudience, "", "", map[string]any{
		"app_client_id":    testClientID,
		"external_user_id": "ext-user-9",
	})
	// Clear sub by rebuilding without subject — signToken always sets Subject.
	// Build manually for empty sub.
	tok, err := jwt.NewBuilder().
		Issuer(testIssuer).
		Audience([]string{testAudience}).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(5*time.Minute)).
		Claim("app_client_id", testClientID).
		Claim("external_user_id", "ext-user-9").
		Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, key))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_ = raw

	clientID, userID, err := v.VerifyUserAccessToken(context.Background(), string(signed), testClientID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if clientID != testClientID || userID != "ext-user-9" {
		t.Fatalf("got client=%q user=%q", clientID, userID)
	}
}

func TestDiscoverJWKSURI(t *testing.T) {
	mux := http.NewServeMux()
	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   issuer + "/",
			"jwks_uri": "https://idp.test/.well-known/jwks.json",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	issuer = srv.URL

	uri, err := discoverJWKSURI(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if uri != "https://idp.test/.well-known/jwks.json" {
		t.Fatalf("jwks_uri=%q", uri)
	}
}

package apikey_test

import (
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
)

func TestGenerateParseVerify(t *testing.T) {
	prefix := "sk_"
	userID := "auth0|abc123"
	plaintext, stored, err := apikey.Generate(prefix, userID)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := apikey.ParseUserID(prefix, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != userID {
		t.Fatalf("parsed user id = %q, want %q", parsed, userID)
	}
	if !apikey.Verify(plaintext, stored) {
		t.Fatal("verify failed")
	}
}

func TestLoadDemoStore(t *testing.T) {
	store, err := apikey.LoadDemoStore(`{"sk_demo":{"clientId":"c1","userId":"u1"}}`)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := store["sk_demo"]
	if !ok || entry.ClientID != "c1" || entry.UserID != "u1" {
		t.Fatalf("unexpected entry: %+v ok=%v", entry, ok)
	}
}

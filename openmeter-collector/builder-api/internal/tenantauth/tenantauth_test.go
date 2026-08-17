package tenantauth_test

import (
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tenantauth"
)

func newAuth() *tenantauth.Authenticator {
	return tenantauth.New("platform-m2m", "platform-secret", map[string]string{
		"tenant-a": "secret-a",
		"tenant-b": "secret-b",
	})
}

func TestPlatformCredentialGetsAdminPrincipal(t *testing.T) {
	p := newAuth().Authenticate("platform-m2m", "platform-secret")
	if p.Kind != tenantauth.KindPlatformAdmin {
		t.Fatalf("expected platform admin, got kind %v", p.Kind)
	}
	for _, clientID := range []string{"tenant-a", "tenant-b", "anything-else"} {
		if !p.CanAccess(clientID) {
			t.Errorf("platform admin should reach %q", clientID)
		}
	}
}

func TestTenantCredentialIsConfinedToItsOwnClient(t *testing.T) {
	p := newAuth().Authenticate("tenant-a", "secret-a")
	if p.Kind != tenantauth.KindTenant || p.ClientID != "tenant-a" {
		t.Fatalf("expected tenant-a principal, got %+v", p)
	}
	if !p.CanAccess("tenant-a") {
		t.Error("tenant-a must reach its own client")
	}
	if p.CanAccess("tenant-b") {
		t.Error("tenant-a must not reach tenant-b")
	}
}

func TestCrossTenantSecretIsRejected(t *testing.T) {
	// tenant-a's id with tenant-b's secret must not authenticate as either.
	if p := newAuth().Authenticate("tenant-a", "secret-b"); p.Authenticated() {
		t.Fatalf("mismatched secret authenticated as %+v", p)
	}
}

func TestUnknownAndEmptyCredentialsAreRejected(t *testing.T) {
	a := newAuth()
	cases := [][2]string{
		{"", ""},
		{"tenant-a", ""},
		{"", "secret-a"},
		{"tenant-c", "secret-c"},
		{"platform-m2m", "wrong"},
		{" ", " "},
	}
	for _, c := range cases {
		if p := a.Authenticate(c[0], c[1]); p.Authenticated() {
			t.Errorf("credentials %q/%q should not authenticate, got %+v", c[0], c[1], p)
		}
	}
}

func TestZeroPrincipalReachesNothing(t *testing.T) {
	var p tenantauth.Principal
	if p.Authenticated() {
		t.Error("zero principal must not be authenticated")
	}
	for _, clientID := range []string{"", "tenant-a", "anything"} {
		if p.CanAccess(clientID) {
			t.Errorf("zero principal must not reach %q", clientID)
		}
	}
}

func TestEmptyPlatformConfigDoesNotMatchEmptyInput(t *testing.T) {
	// A deployment with no platform credentials configured must not turn empty
	// credentials into platform admin.
	a := tenantauth.New("", "", nil)
	if p := a.Authenticate("", ""); p.Authenticated() {
		t.Fatalf("empty config authenticated empty credentials as %+v", p)
	}
}

func TestCanAccessRejectsEmptyTarget(t *testing.T) {
	if newAuth().Authenticate("platform-m2m", "platform-secret").CanAccess("") {
		t.Error("empty client id must never be reachable, even by platform admin")
	}
}

func TestParseTenantSecrets(t *testing.T) {
	got, err := tenantauth.ParseTenantSecrets(`{"a":"sa","b":"sb"," ":"skip","c":""}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 || got["a"] != "sa" || got["b"] != "sb" {
		t.Fatalf("unexpected parse result: %+v", got)
	}

	empty, err := tenantauth.ParseTenantSecrets("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty input should yield empty map, got %+v err=%v", empty, err)
	}

	if _, err := tenantauth.ParseTenantSecrets("not json"); err == nil {
		t.Error("malformed JSON should error")
	}
}

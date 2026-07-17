package identity_test

import (
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/identity"
)

func TestCustomerKeyM2MUnchanged(t *testing.T) {
	t.Parallel()
	got := identity.CustomerKey(" pub-client ", " demo-user ")
	if got != "pub-client:demo-user" {
		t.Fatalf("CustomerKey() = %q", got)
	}
}

func TestCustomerKeyOwnerWireSubject(t *testing.T) {
	t.Parallel()
	ownerID := "2e51154b-d296-4015-990c-02d5f16ecf1e"
	got := identity.CustomerKey("app_abc", "owner:"+ownerID)
	if got != ownerID {
		t.Fatalf("owner CustomerKey() = %q, want bare id %q", got, ownerID)
	}
}

func TestParseOwnerWireSubject(t *testing.T) {
	t.Parallel()
	id, ok := identity.ParseOwnerWireSubject("owner:uuid-1")
	if !ok || id != "uuid-1" {
		t.Fatalf("ParseOwnerWireSubject = (%q, %v)", id, ok)
	}
	if identity.IsOwnerWireSubject("app_abc:user-1") {
		t.Fatal("compound key should not be owner wire subject")
	}
	if _, ok := identity.ParseOwnerWireSubject("owner:"); ok {
		t.Fatal("empty owner id should not parse")
	}
}

func TestCloudEventSubject(t *testing.T) {
	t.Parallel()
	ownerID := "2e51154b-d296-4015-990c-02d5f16ecf1e"

	cases := []struct {
		authID string
		want   string
	}{
		{"demo-client:demo-user", "demo-client:demo-user"},
		{"app_abc:owner:" + ownerID, ownerID},
		{"bare-subject", "bare-subject"},
	}
	for _, tc := range cases {
		if got := identity.CloudEventSubject(tc.authID); got != tc.want {
			t.Fatalf("CloudEventSubject(%q) = %q, want %q", tc.authID, got, tc.want)
		}
	}
}

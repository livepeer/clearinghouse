// Package identity defines the OpenMeter customer / CloudEvent subject key contract
// shared by the collector mapping and bootstrap provisioning.
package identity

import "strings"

// OwnerWirePrefix is the transport marker webhook → go-livepeer stamps on
// app-owner usage subjects (auth_id = app_…:owner:{users.id}). The collector
// strips this before writing the CloudEvent subject; Konnect customer keys use
// the bare platform user id.
const OwnerWirePrefix = "owner:"

// CustomerKey returns the deterministic OpenMeter customer / CloudEvent subject key.
//
// M2M / managed users: compound clientID:externalUserID (unchanged).
// App owners: bare {users.id} when externalUserID is the owner: wire subject.
func CustomerKey(clientID, externalUserID string) string {
	clientID = strings.TrimSpace(clientID)
	externalUserID = strings.TrimSpace(externalUserID)
	if ownerID, ok := ParseOwnerWireSubject(externalUserID); ok {
		return ownerID
	}
	return clientID + ":" + externalUserID
}

// ParseOwnerWireSubject returns the bare user id when s is owner:{id}.
func ParseOwnerWireSubject(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, OwnerWirePrefix) {
		return "", false
	}
	id := strings.TrimPrefix(s, OwnerWirePrefix)
	if id == "" {
		return "", false
	}
	return id, true
}

// IsOwnerWireSubject reports whether s is the owner:{id} transport marker.
func IsOwnerWireSubject(s string) bool {
	_, ok := ParseOwnerWireSubject(s)
	return ok
}

// CloudEventSubject is the OpenMeter attribution subject for a Kafka auth_id.
// Equivalent to CustomerKey after splitting auth_id on the first colon.
func CloudEventSubject(authID string) string {
	authID = strings.TrimSpace(authID)
	colon := strings.Index(authID, ":")
	if colon <= 0 || colon >= len(authID)-1 {
		return authID
	}
	clientID := authID[:colon]
	usageSubject := authID[colon+1:]
	return CustomerKey(clientID, usageSubject)
}

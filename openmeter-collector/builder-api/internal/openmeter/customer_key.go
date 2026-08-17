package openmeter

import "strings"

// CustomerKey returns the deterministic OpenMeter customer / usage subject key.
// This must match the CloudEvent subject and identity-webhook auth_id compound id.
func CustomerKey(clientID, externalUserID string) string {
	return strings.TrimSpace(clientID) + ":" + strings.TrimSpace(externalUserID)
}

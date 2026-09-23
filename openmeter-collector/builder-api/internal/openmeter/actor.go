package openmeter

import "strings"

const ownerCustomerKeyPrefix = "owner:"

// NormalizePlatformUserID strips owner:/user: prefixes used on wire subjects.
// End-user keys (eu_…) are left unchanged.
func NormalizePlatformUserID(externalUserID string) string {
	trimmed := strings.TrimSpace(externalUserID)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "eu_") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, ownerCustomerKeyPrefix) {
		return trimmed[len(ownerCustomerKeyPrefix):]
	}
	if strings.HasPrefix(trimmed, "user:") {
		return trimmed[len("user:"):]
	}
	return trimmed
}

// ExternalUserIDMatchKeys returns values that may appear in meter
// groupBy.external_user_id for one actor (aligned with pymthouse usage-read).
func ExternalUserIDMatchKeys(externalUserID string) map[string]struct{} {
	trimmed := strings.TrimSpace(externalUserID)
	keys := make(map[string]struct{})
	if trimmed == "" {
		return keys
	}
	normalized := NormalizePlatformUserID(trimmed)
	keys[trimmed] = struct{}{}
	keys[normalized] = struct{}{}
	keys[ownerCustomerKeyPrefix+normalized] = struct{}{}
	keys["user:"+normalized] = struct{}{}
	return keys
}

// RowMatchesActor keeps rows whose client_id matches and whose external_user_id
// is one of the actor's match keys. Rows missing those dimensions are dropped.
func RowMatchesActor(row UsageRow, clientID, actorExternalUserID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false
	}
	group := row.GroupBy
	if group == nil {
		group = map[string]string{}
	}
	rowClient := strings.TrimSpace(group["client_id"])
	if rowClient == "" && strings.HasPrefix(row.Subject, clientID+":") {
		// Legacy subject-shaped rows without dimension maps.
		rowClient = clientID
	}
	if rowClient != clientID {
		return false
	}

	matchKeys := ExternalUserIDMatchKeys(actorExternalUserID)
	if len(matchKeys) == 0 {
		return false
	}
	raw := strings.TrimSpace(group["external_user_id"])
	if raw == "" && strings.HasPrefix(row.Subject, clientID+":") {
		raw = strings.TrimPrefix(row.Subject, clientID+":")
	}
	if raw == "" {
		return false
	}
	if _, ok := matchKeys[raw]; ok {
		return true
	}
	if _, ok := matchKeys[NormalizePlatformUserID(raw)]; ok {
		return true
	}
	return false
}

package openmeter

import "strings"

// Catalog meters and dimensions mirror openmeter-collector/provision/catalog.json.
// Keep this map in sync with that file (see catalog_test.go).
var catalogMeters = map[string]map[string]struct{}{
	"network_fee_usd_micros": {
		"client_id": {}, "external_user_id": {}, "model_id": {}, "pipeline": {},
	},
	"billable_usd_micros": {
		"client_id": {}, "external_user_id": {}, "model_id": {}, "pipeline": {},
	},
	"signed_ticket_count": {
		"client_id": {}, "external_user_id": {}, "model_id": {}, "pipeline": {},
	},
	"fee_wei": {
		"client_id": {}, "external_user_id": {}, "model_id": {}, "pipeline": {}, "manifest_id": {},
	},
	"billable_secs": {
		"client_id": {}, "external_user_id": {}, "model_id": {}, "pipeline": {}, "manifest_id": {},
	},
	"network_fee_usd_micros_by_manifest": {
		"client_id": {}, "external_user_id": {}, "model_id": {}, "pipeline": {}, "manifest_id": {},
	},
}

// CatalogMeterKnown reports whether meter is a provisioned catalog key.
func CatalogMeterKnown(meter string) bool {
	_, ok := catalogMeters[strings.TrimSpace(meter)]
	return ok
}

// CatalogDimensionKnown reports whether dim is a dimension on meter.
func CatalogDimensionKnown(meter, dim string) bool {
	dims, ok := catalogMeters[strings.TrimSpace(meter)]
	if !ok {
		return false
	}
	_, ok = dims[strings.TrimSpace(dim)]
	return ok
}

// DefaultUsageGroupBy returns the minimum group-by set for actor-scoped reads.
func DefaultUsageGroupBy(meter string) []string {
	dims, ok := catalogMeters[strings.TrimSpace(meter)]
	if !ok {
		return []string{"client_id", "external_user_id"}
	}
	out := make([]string, 0, 2)
	if _, ok := dims["client_id"]; ok {
		out = append(out, "client_id")
	}
	if _, ok := dims["external_user_id"]; ok {
		out = append(out, "external_user_id")
	}
	return out
}

// ValidateUsageGroupBy returns an error string when any groupBy dim is not on the meter.
func ValidateUsageGroupBy(meter string, groupBy []string) string {
	for _, g := range groupBy {
		g = strings.TrimSpace(g)
		if g == "" || g == "subject" {
			continue
		}
		if !CatalogDimensionKnown(meter, g) {
			return "groupBy dimension is not allowed for this meter: " + g
		}
	}
	return ""
}

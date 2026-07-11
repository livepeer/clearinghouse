package openmeter

import (
	"context"
	"fmt"
	"strings"
)

// SessionService provisions customers/subscriptions and reads balance using
// per-tenant Konnect credentials (or a single-org fallback).
type SessionService struct {
	Resolver *Resolver
}

// NewSessionService wraps a credential resolver.
func NewSessionService(resolver *Resolver) *SessionService {
	return &SessionService{Resolver: resolver}
}

// ProvisionSession upserts customer + subscription, optionally grants trial credits,
// and returns the current access snapshot.
func (s *SessionService) ProvisionSession(ctx context.Context, cfg ProvisionConfig, clientID, externalUserID string) (*SessionProvision, error) {
	if s == nil || s.Resolver == nil {
		return nil, fmt.Errorf("openmeter session service is not configured")
	}
	client, err := s.Resolver.ClientFor(ctx, clientID)
	if err != nil {
		return nil, err
	}

	customer, err := client.EnsureCustomer(ctx, clientID, externalUserID, externalUserID)
	if err != nil {
		return nil, err
	}
	customerKey := CustomerKey(clientID, externalUserID)
	if err := client.EnsureDefaultSubscription(ctx, customer.ID, customerKey, cfg.DefaultPlanKey); err != nil {
		return nil, err
	}

	featureKey := strings.TrimSpace(cfg.TrialFeatureKey)
	if featureKey == "" {
		featureKey = "billable_spend"
	}

	if cfg.TrialGrantUSDMicros > 0 {
		grantKey := fmt.Sprintf("trial:%s", customerKey)
		if err := client.EnsureTrialGrant(ctx, customer.ID, featureKey, grantKey, cfg.TrialGrantUSDMicros); err != nil {
			return nil, err
		}
	}

	access, err := client.GetAccess(ctx, customer.ID, featureKey)
	if err != nil {
		return nil, err
	}
	if access == nil {
		access = &Access{HasAccess: false, BalanceUSDMicros: 0, Source: "none"}
	}

	return &SessionProvision{
		Customer:         customer,
		CustomerKey:      customerKey,
		HasAccess:        access.HasAccess,
		BalanceUSDMicros: access.BalanceUSDMicros,
		BalanceSource:    access.Source,
	}, nil
}

package main

import (
	"context"
	"fmt"
	"strings"

	sdkkonnectgo "github.com/Kong/sdk-konnect-go"
	"github.com/Kong/sdk-konnect-go/models/components"
	"github.com/Kong/sdk-konnect-go/models/operations"
)

type Provisioner struct {
	sdk     *sdkkonnectgo.SDK
	planKey string
}

type ProvisionInput struct {
	ClientID       string
	ExternalUserID string
	DisplayName    string
}

type ProvisionResult struct {
	CustomerKey    string `json:"customerKey"`
	CustomerID     string `json:"customerId"`
	SubscriptionID string `json:"subscriptionId"`
	PlanKey        string `json:"planKey"`
	Status         string `json:"status"`
	Created        struct {
		Customer     bool `json:"customer"`
		Subscription bool `json:"subscription"`
	} `json:"created"`
}

func buildCustomerKey(clientID, externalUserID string) string {
	return strings.TrimSpace(clientID) + ":" + strings.TrimSpace(externalUserID)
}

func normalizeOpenMeterURL(baseURL string) string {
	url := strings.TrimSpace(baseURL)
	url = strings.TrimSuffix(url, "/")
	url = strings.TrimSuffix(url, "/events")
	url = strings.TrimSuffix(url, "/openmeter")
	url = strings.TrimSuffix(url, "/v3")
	return url
}

func NewProvisioner(baseURL, apiKey, planKey string) *Provisioner {
	url := normalizeOpenMeterURL(baseURL)
	opts := []sdkkonnectgo.SDKOption{
		sdkkonnectgo.WithSecurity(components.Security{
			PersonalAccessToken: sdkkonnectgo.Pointer(apiKey),
		}),
	}
	if url != "" {
		opts = append(opts, sdkkonnectgo.WithServerURL(url))
	} else {
		opts = append(opts, sdkkonnectgo.WithServerIndex(1))
	}
	return &Provisioner{
		sdk:     sdkkonnectgo.New(opts...),
		planKey: strings.TrimSpace(planKey),
	}
}

func (p *Provisioner) Ensure(ctx context.Context, input ProvisionInput) (*ProvisionResult, error) {
	clientID := strings.TrimSpace(input.ClientID)
	externalUserID := strings.TrimSpace(input.ExternalUserID)
	if clientID == "" || externalUserID == "" {
		return nil, fmt.Errorf("clientId and externalUserId must be non-empty")
	}
	if p.planKey == "" {
		return nil, fmt.Errorf("plan key must be non-empty")
	}

	customerKey := buildCustomerKey(clientID, externalUserID)
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = customerKey
	}

	customerID, customerCreated, err := p.ensureCustomer(ctx, customerKey, displayName)
	if err != nil {
		return nil, err
	}

	subscriptionID, status, subscriptionCreated, err := p.ensureSubscription(ctx, customerID)
	if err != nil {
		return nil, err
	}

	result := &ProvisionResult{
		CustomerKey:    customerKey,
		CustomerID:     customerID,
		SubscriptionID: subscriptionID,
		PlanKey:        p.planKey,
		Status:         status,
	}
	result.Created.Customer = customerCreated
	result.Created.Subscription = subscriptionCreated
	return result, nil
}

func (p *Provisioner) ensureCustomer(ctx context.Context, customerKey, displayName string) (string, bool, error) {
	if existing, err := p.findCustomerByKey(ctx, customerKey); err != nil {
		return "", false, err
	} else if existing != "" {
		return existing, false, nil
	}

	res, err := p.sdk.OpenMeterCustomers.CreateCustomer(ctx, components.CreateCustomerRequest{
		Key:  customerKey,
		Name: displayName,
		UsageAttribution: &components.UsageAttribution{
			SubjectKeys: []string{customerKey},
		},
	})
	if err != nil {
		if existing, findErr := p.findCustomerByKey(ctx, customerKey); findErr == nil && existing != "" {
			return existing, false, nil
		}
		return "", false, fmt.Errorf("creating customer %s: %w", customerKey, err)
	}
	if res.BillingCustomer == nil || res.BillingCustomer.ID == "" {
		return "", false, fmt.Errorf("creating customer %s: empty response", customerKey)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", false, fmt.Errorf("creating customer %s: status %d", customerKey, res.StatusCode)
	}
	return res.BillingCustomer.ID, true, nil
}

func (p *Provisioner) findCustomerByKey(ctx context.Context, customerKey string) (string, error) {
	res, err := p.sdk.OpenMeterCustomers.ListCustomers(ctx, operations.ListCustomersRequest{
		Filter: &components.ListCustomersParamsFilter{
			Key: &components.StringFieldFilter{
				Eq: sdkkonnectgo.Pointer(customerKey),
			},
		},
		Page: &components.PagePaginationQuery{
			Number: sdkkonnectgo.Pointer(int64(1)),
			Size:   sdkkonnectgo.Pointer(int64(100)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("listing customers for key %s: %w", customerKey, err)
	}
	if res.CustomerPagePaginatedResponse != nil {
		for _, customer := range res.CustomerPagePaginatedResponse.Data {
			if customer.Key == customerKey && customer.ID != "" {
				return customer.ID, nil
			}
		}
	}

	getRes, err := p.sdk.OpenMeterCustomers.GetCustomer(ctx, customerKey)
	if err == nil && getRes.BillingCustomer != nil && getRes.BillingCustomer.ID != "" {
		return getRes.BillingCustomer.ID, nil
	}
	if err == nil && (getRes.StatusCode == 404) {
		return "", nil
	}
	if err == nil && getRes.StatusCode >= 200 && getRes.StatusCode < 300 {
		return "", nil
	}
	if err != nil {
		return "", nil
	}
	return "", nil
}

func subscriptionStatusActive(status components.BillingSubscriptionStatus) bool {
	switch status {
	case components.BillingSubscriptionStatusActive,
		components.BillingSubscriptionStatusScheduled:
		return true
	default:
		return false
	}
}

func planStatusUsable(status components.BillingPlanStatus) bool {
	switch status {
	case components.BillingPlanStatusActive,
		components.BillingPlanStatusScheduled:
		return true
	default:
		return false
	}
}

func (p *Provisioner) resolveActivePlan(ctx context.Context) (components.Plan, error) {
	const pageSize = int64(50)
	page := int64(1)
	var best *components.BillingPlan

	for {
		res, err := p.sdk.OpenMeterProductCatalog.ListPlans(ctx, operations.ListPlansRequest{
			Filter: &components.ListPlansParamsFilter{
				Key: &components.StringFieldFilter{
					Eq: sdkkonnectgo.Pointer(p.planKey),
				},
			},
			Page: &components.PagePaginationQuery{
				Number: sdkkonnectgo.Pointer(page),
				Size:   sdkkonnectgo.Pointer(pageSize),
			},
		})
		if err != nil {
			return components.Plan{}, fmt.Errorf("listing plans for key %s: %w", p.planKey, err)
		}
		if res.PlanPagePaginatedResponse == nil {
			break
		}
		for i := range res.PlanPagePaginatedResponse.Data {
			plan := res.PlanPagePaginatedResponse.Data[i]
			if plan.DeletedAt != nil || !planStatusUsable(plan.Status) || plan.ID == "" {
				continue
			}
			if best == nil || planVersion(plan) > planVersion(*best) {
				copy := plan
				best = &copy
			}
		}
		if len(res.PlanPagePaginatedResponse.Data) < int(pageSize) {
			break
		}
		page++
	}

	if best == nil {
		return components.Plan{}, fmt.Errorf("no active plan found for key %s (latest version may be deleted; re-run clearinghouse-bootstrap --skip-auth0)", p.planKey)
	}

	return components.Plan{
		ID:      sdkkonnectgo.Pointer(best.ID),
		Key:     sdkkonnectgo.Pointer(p.planKey),
		Version: best.Version,
	}, nil
}

func planVersion(plan components.BillingPlan) int64 {
	if plan.Version == nil {
		return 0
	}
	return *plan.Version
}

func (p *Provisioner) ensureSubscription(ctx context.Context, customerID string) (string, string, bool, error) {
	const pageSize = int64(100)
	page := int64(1)

	for {
		customerFilter := components.CreateULIDFieldFilterStr(customerID)
		res, err := p.sdk.OpenMeterSubscriptions.ListSubscriptions(ctx, operations.ListSubscriptionsRequest{
			Filter: &components.ListSubscriptionsParamsFilter{
				CustomerID: &customerFilter,
				PlanKey: &components.StringFieldFilterExact{
					Eq: sdkkonnectgo.Pointer(p.planKey),
				},
			},
			Page: &components.PagePaginationQuery{
				Number: sdkkonnectgo.Pointer(page),
				Size:   sdkkonnectgo.Pointer(pageSize),
			},
		})
		if err != nil {
			return "", "", false, fmt.Errorf("listing subscriptions for customer %s: %w", customerID, err)
		}
		if res.SubscriptionPagePaginatedResponse != nil {
			for _, sub := range res.SubscriptionPagePaginatedResponse.Data {
				if sub.ID != "" && subscriptionStatusActive(sub.Status) {
					return sub.ID, string(sub.Status), false, nil
				}
			}
			if len(res.SubscriptionPagePaginatedResponse.Data) < int(pageSize) {
				break
			}
		} else {
			break
		}
		page++
	}

	planRef, err := p.resolveActivePlan(ctx)
	if err != nil {
		return "", "", false, err
	}

	createRes, err := p.sdk.OpenMeterSubscriptions.CreateSubscription(ctx, components.BillingSubscriptionCreate{
		Customer: components.BillingSubscriptionCreateCustomer{
			ID: sdkkonnectgo.Pointer(customerID),
		},
		Plan: planRef,
	})
	if err != nil {
		return "", "", false, fmt.Errorf("creating subscription for customer %s plan %s: %w", customerID, p.planKey, err)
	}
	if createRes.BillingSubscription == nil || createRes.BillingSubscription.ID == "" {
		return "", "", false, fmt.Errorf("creating subscription for customer %s plan %s: empty response", customerID, p.planKey)
	}
	if createRes.StatusCode < 200 || createRes.StatusCode >= 300 {
		return "", "", false, fmt.Errorf("creating subscription for customer %s plan %s: status %d", customerID, p.planKey, createRes.StatusCode)
	}
	return createRes.BillingSubscription.ID, string(createRes.BillingSubscription.Status), true, nil
}

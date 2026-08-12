package openmeter

import "context"

// ProvisionSession upserts the customer and ensures a default-plan subscription.
func (c *Client) ProvisionSession(ctx context.Context, cfg ProvisionConfig, clientID, externalUserID string) (*SessionProvision, error) {
	customerKey := CustomerKey(clientID, externalUserID)

	customer, err := c.EnsureCustomer(ctx, clientID, externalUserID, externalUserID)
	if err != nil {
		return nil, err
	}

	if err := c.EnsureDefaultSubscription(ctx, customer.ID, customerKey, cfg.DefaultPlanKey); err != nil {
		return nil, err
	}

	return &SessionProvision{
		Customer:    customer,
		CustomerKey: customerKey,
	}, nil
}

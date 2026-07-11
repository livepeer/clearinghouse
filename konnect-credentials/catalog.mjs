import { readFile } from "node:fs/promises";
import { unwrapList, unwrapOne } from "./konnect-client.mjs";

/**
 * Idempotent catalog bootstrap against a tenant org (mirrors bootstrap.sh catalog).
 */
export async function bootstrapCatalog(client, catalogPath) {
  const catalog = JSON.parse(await readFile(catalogPath, "utf8"));
  const meters = await ensureMeters(client, catalog.meters || []);
  const features = await ensureFeatures(client, catalog.features || []);
  const plan = catalog.plan
    ? await ensurePlan(client, catalog.plan)
    : null;
  return {
    meters,
    features,
    plan,
  };
}

async function ensureMeters(client, meters) {
  const existing = unwrapList(await client.listMeters());
  const byKey = new Map(existing.map((m) => [m.key || m.slug, m]));
  const results = [];
  for (const meter of meters) {
    if (byKey.has(meter.key)) {
      results.push({ key: meter.key, status: "exists", id: byKey.get(meter.key).id });
      continue;
    }
    const body = {
      slug: meter.key,
      name: meter.name,
      description: meter.description,
      eventType: meter.event_type,
      aggregation: meter.aggregation,
      valueProperty: meter.value_property,
      groupBy: meter.dimensions || undefined,
    };
    // Konnect may expect snake_case — try camel first, fall back.
    try {
      const created = unwrapOne(await client.createMeter(body));
      results.push({ key: meter.key, status: "created", id: created.id });
    } catch (err) {
      if (err.status === 400 || err.status === 422) {
        const snake = {
          slug: meter.key,
          name: meter.name,
          description: meter.description,
          event_type: meter.event_type,
          aggregation: meter.aggregation,
          value_property: meter.value_property,
          group_by: meter.dimensions || undefined,
        };
        const created = unwrapOne(await client.createMeter(snake));
        results.push({ key: meter.key, status: "created", id: created.id });
      } else if (err.status === 409) {
        results.push({ key: meter.key, status: "exists" });
      } else {
        throw err;
      }
    }
  }
  return results;
}

async function ensureFeatures(client, features) {
  const existing = unwrapList(await client.listFeatures());
  const byKey = new Map(existing.map((f) => [f.key, f]));
  const results = [];
  for (const feature of features) {
    if (byKey.has(feature.key)) {
      results.push({ key: feature.key, status: "exists", id: byKey.get(feature.key).id });
      continue;
    }
    const body = {
      key: feature.key,
      name: feature.name,
      meterSlug: feature.meter_key,
    };
    try {
      const created = unwrapOne(await client.createFeature(body));
      results.push({ key: feature.key, status: "created", id: created.id });
    } catch (err) {
      if (err.status === 400 || err.status === 422) {
        const snake = {
          key: feature.key,
          name: feature.name,
          meter_slug: feature.meter_key,
        };
        const created = unwrapOne(await client.createFeature(snake));
        results.push({ key: feature.key, status: "created", id: created.id });
      } else if (err.status === 409) {
        results.push({ key: feature.key, status: "exists" });
      } else {
        throw err;
      }
    }
  }
  return results;
}

async function ensurePlan(client, plan) {
  const existing = unwrapList(await client.listPlans());
  const found = existing.find((p) => p.key === plan.key);
  if (found) {
    const version = found.version || found.versions?.[0];
    const status = found.status || version?.status;
    if (status === "active" || status === "published") {
      return { key: plan.key, status: "exists", id: found.id };
    }
    // Try publish if draft
    if (found.id) {
      try {
        await client.publishPlan(found.id);
        return { key: plan.key, status: "published", id: found.id };
      } catch {
        return { key: plan.key, status: "exists", id: found.id };
      }
    }
    return { key: plan.key, status: "exists", id: found.id };
  }

  const phase = plan.phases?.[0];
  const rateCards = (phase?.rate_cards || []).map((rc) => ({
    type: "usage_based",
    key: rc.key,
    name: rc.name,
    featureKey: rc.feature_key,
    billingCadence: rc.billing_cadence || plan.billing_cadence,
    price: rc.price,
  }));

  const body = {
    key: plan.key,
    name: plan.name,
    description: plan.description,
    currency: plan.currency || "USD",
    billingCadence: plan.billing_cadence,
    phases: [
      {
        key: phase?.key || "default",
        name: phase?.name || "Default",
        rateCards,
      },
    ],
  };

  let created;
  try {
    created = unwrapOne(await client.createPlan(body));
  } catch (err) {
    if (err.status === 400 || err.status === 422) {
      const snake = {
        key: plan.key,
        name: plan.name,
        description: plan.description,
        currency: plan.currency || "USD",
        billing_cadence: plan.billing_cadence,
        phases: [
          {
            key: phase?.key || "default",
            name: phase?.name || "Default",
            rate_cards: (phase?.rate_cards || []).map((rc) => ({
              type: "usage_based",
              key: rc.key,
              name: rc.name,
              feature_key: rc.feature_key,
              billing_cadence: rc.billing_cadence || plan.billing_cadence,
              price: rc.price,
            })),
          },
        ],
      };
      created = unwrapOne(await client.createPlan(snake));
    } else {
      throw err;
    }
  }

  if (created?.id) {
    try {
      await client.publishPlan(created.id);
      return { key: plan.key, status: "created_published", id: created.id };
    } catch {
      return { key: plan.key, status: "created", id: created.id };
    }
  }
  return { key: plan.key, status: "created", id: created?.id };
}

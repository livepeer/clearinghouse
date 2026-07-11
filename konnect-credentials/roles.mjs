/**
 * Konnect Metering & Billing RBAC payloads for system-account role assignment.
 *
 * Spiked against a live org: entity_type_name is "Metering", entity_id "*",
 * entity_region "*". See Konnect teams-and-roles docs.
 */

export const METERING_ENTITY = {
  entity_type_name: "Metering",
  entity_id: "*",
  entity_region: "*",
};

/** Roles handed to the tenant Provisioner SPAT. */
export const PROVISIONER_ROLES = [
  "Billing Admin",
  "Product Catalog Admin",
  "Metering Admin",
];

/** Roles handed to the tenant Usage SPAT. */
export const USAGE_ROLES = [
  "Metering Viewer",
  "Billing Viewer",
];

/** Roles for the platform-held Ingest SPAT (never returned to tenants). */
export const INGEST_ROLES = [
  "Ingest",
];

export const ACCOUNT_NAMES = {
  provisioner: "ch-provisioner",
  usage: "ch-usage",
  ingest: "ch-ingest",
};

export function roleAssignment(roleName) {
  return {
    role_name: roleName,
    ...METERING_ENTITY,
  };
}

export function rolesForKind(kind) {
  switch (kind) {
    case "provisioner":
      return PROVISIONER_ROLES;
    case "usage":
      return USAGE_ROLES;
    case "ingest":
      return INGEST_ROLES;
    default:
      throw new Error(`unknown credential kind: ${kind}`);
  }
}

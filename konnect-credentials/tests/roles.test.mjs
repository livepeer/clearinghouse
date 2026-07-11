import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
  ACCOUNT_NAMES,
  INGEST_ROLES,
  METERING_ENTITY,
  PROVISIONER_ROLES,
  USAGE_ROLES,
  roleAssignment,
  rolesForKind,
} from "../roles.mjs";

describe("roles", () => {
  it("uses Metering entity_type_name with wildcard id/region", () => {
    assert.equal(METERING_ENTITY.entity_type_name, "Metering");
    assert.equal(METERING_ENTITY.entity_id, "*");
    assert.equal(METERING_ENTITY.entity_region, "*");
  });

  it("builds role assignment payloads", () => {
    assert.deepEqual(roleAssignment("Ingest"), {
      role_name: "Ingest",
      entity_type_name: "Metering",
      entity_id: "*",
      entity_region: "*",
    });
  });

  it("maps kinds to role sets", () => {
    assert.deepEqual(rolesForKind("provisioner"), PROVISIONER_ROLES);
    assert.deepEqual(rolesForKind("usage"), USAGE_ROLES);
    assert.deepEqual(rolesForKind("ingest"), INGEST_ROLES);
    assert.equal(ACCOUNT_NAMES.provisioner, "ch-provisioner");
  });
});

import {
  ACCOUNT_NAMES,
  roleAssignment,
  rolesForKind,
} from "./roles.mjs";
import { unwrapList, unwrapOne } from "./konnect-client.mjs";

/**
 * Ensure three system accounts exist, assign roles, mint SPATs.
 * Returns provisioner + usage secrets (once) and ingest secret for platform storage.
 */
export async function provisionCredentials(client, {
  tokenNameSuffix = "default",
}) {
  const accounts = {};
  for (const kind of ["provisioner", "usage", "ingest"]) {
    accounts[kind] = await ensureSystemAccount(client, kind);
    await ensureRoles(client, accounts[kind].id, rolesForKind(kind));
  }

  const tokens = {};
  for (const kind of ["provisioner", "usage", "ingest"]) {
    tokens[kind] = await mintToken(client, accounts[kind].id, `${kind}-${tokenNameSuffix}`);
  }

  return {
    accounts: {
      provisioner: { id: accounts.provisioner.id, name: accounts.provisioner.name },
      usage: { id: accounts.usage.id, name: accounts.usage.name },
      ingest: { id: accounts.ingest.id, name: accounts.ingest.name },
    },
    tokens: {
      provisioner: {
        id: tokens.provisioner.id,
        name: tokens.provisioner.name,
        token: tokens.provisioner.token,
      },
      usage: {
        id: tokens.usage.id,
        name: tokens.usage.name,
        token: tokens.usage.token,
      },
      ingest: {
        id: tokens.ingest.id,
        name: tokens.ingest.name,
        token: tokens.ingest.token,
      },
    },
  };
}

/**
 * Rotate one credential kind: delete old token (if known), mint a new SPAT.
 */
export async function rotateCredential(client, {
  kind,
  accountId,
  previousTokenId,
  tokenNameSuffix = `rot-${Date.now()}`,
}) {
  if (previousTokenId) {
    try {
      await client.deleteAccessToken(accountId, previousTokenId);
    } catch (err) {
      if (err.status !== 404) {
        throw err;
      }
    }
  }
  const minted = await mintToken(client, accountId, `${kind}-${tokenNameSuffix}`);
  return {
    kind,
    account_id: accountId,
    id: minted.id,
    name: minted.name,
    token: minted.token,
  };
}

async function ensureSystemAccount(client, kind) {
  const name = ACCOUNT_NAMES[kind];
  const listed = unwrapList(await client.listSystemAccounts());
  const existing = listed.find((a) => a.name === name);
  if (existing) {
    return existing;
  }
  try {
    const created = unwrapOne(await client.createSystemAccount({
      name,
      description: `Clearinghouse ${kind} system account`,
    }));
    return created;
  } catch (err) {
    if (err.status === 409) {
      const again = unwrapList(await client.listSystemAccounts());
      const found = again.find((a) => a.name === name);
      if (found) {
        return found;
      }
    }
    throw err;
  }
}

async function ensureRoles(client, accountId, roleNames) {
  const existing = unwrapList(await client.listAssignedRoles(accountId));
  const have = new Set(
    existing.map((r) => `${r.role_name}|${r.entity_type_name}|${r.entity_id}`),
  );
  for (const roleName of roleNames) {
    const assignment = roleAssignment(roleName);
    const key = `${assignment.role_name}|${assignment.entity_type_name}|${assignment.entity_id}`;
    if (have.has(key)) {
      continue;
    }
    try {
      await client.assignRole(accountId, assignment);
    } catch (err) {
      if (err.status !== 409) {
        throw err;
      }
    }
  }
}

async function mintToken(client, accountId, name) {
  const created = unwrapOne(await client.createAccessToken(accountId, { name }));
  const token = created.token || created.access_token || created.value;
  if (!token) {
    throw new Error(`Konnect access token response missing secret for ${name}`);
  }
  return {
    id: created.id,
    name: created.name || name,
    token,
  };
}

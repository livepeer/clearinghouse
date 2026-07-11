# Releasing `@livepeer/clearinghouse-identity-webhook`

Releases are triggered by pushing a semver tag (`v*.*.*`) on the `livepeer/clearinghouse`
repository. The [release workflow](../../.github/workflows/release.yml) runs tests,
publishes to npm via **trusted publishing** (OIDC), and creates a GitHub Release.

Tags apply to the **identity-webhook** npm package (monorepo subdirectory).

## npm trusted publishing (required)

This package publishes with [npm trusted publishing](https://docs.npmjs.com/trusted-publishers)
— no `NPM_TOKEN` secret on the publish step.

### First publish (bootstrap)

npm trusted publishing cannot create a brand-new scoped package (first OIDC publish
returns E404). For the very first release only:

1. Create a granular npm token with Read and Write on `@livepeer`, and enable
   **Bypass two-factor authentication for automation**.
2. `gh secret set NPM_TOKEN -R livepeer/clearinghouse`
3. Tag `v0.3.0` (must match `package.json`), then **Actions → bootstrap-publish →
   Run workflow** with that tag.
4. On npmjs.com, configure trusted publishing for `livepeer/clearinghouse` /
   `release.yml` (steps below).
5. **Delete** the `NPM_TOKEN` repository secret. Subsequent releases use `release.yml`
   only.

### Trusted publishing setup on npmjs.com

1. Create or open **@livepeer/clearinghouse-identity-webhook** on npmjs.com (under the
   `@livepeer` org) — or let bootstrap-publish create it on first run.
2. Open **Settings** → **Trusted publishing**.
3. Add a **GitHub Actions** publisher:
   - **Repository:** `livepeer/clearinghouse`
   - **Workflow filename:** `release.yml` (exact name, including `.yml`)
   - **Environment:** leave empty unless you use a GitHub Environment
4. **Remove** the `NPM_TOKEN` repository secret if it still exists. A leftover token
   overrides OIDC and causes `npm error code EOTP`.
5. Optional: **Publishing access** → disallow traditional tokens once publishes succeed.

### Workflow requirements (already in `release.yml`)

- `permissions.id-token: write`
- `actions/setup-node` with `registry-url: https://registry.npmjs.org` and `scope: "@livepeer"`
- **No** `NODE_AUTH_TOKEN` / `NPM_TOKEN` on the publish step
- `npm publish` (npm CLI ≥ 11.5.1), not `pnpm publish`

`npm whoami` does not reflect OIDC auth; a failed publish usually means the trusted
publisher fields do not match the workflow run (repo, workflow file name, or tag vs
`workflow_dispatch`).

## Re-run a failed release

If the tag already exists (e.g. `v0.3.0`) but npm publish failed:

1. Confirm trusted publishing and delete `NPM_TOKEN` if present.
2. **Actions** → **release** → **Run workflow** → tag `v0.3.0` → **Run workflow**.

## Cutting a new version

Use the **Bump version** workflow or locally:

```bash
cd identity-webhook
npm version patch   # or minor / major / prerelease
git push origin main --tags
```

The tag push starts **release** automatically.

## Local dry-run

```bash
cd identity-webhook
npm ci
npm test
npm pack --dry-run
```

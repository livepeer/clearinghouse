# Releasing `@pymthouse/clearinghouse-identity-webhook`

Publishing runs from [livepeer/clearinghouse](https://github.com/livepeer/clearinghouse)
via the [release workflow](../../.github/workflows/release.yml). The package keeps
the `@pymthouse` npm scope; only the GitHub publisher moved off the fork.

## npm trusted publishing (required)

Configure [npm trusted publishing](https://docs.npmjs.com/trusted-publishers) on
**@pymthouse/clearinghouse-identity-webhook**:

1. npmjs.com → package → **Settings** → **Trusted publishing**
2. **GitHub Actions** publisher:
   - **Repository:** `livepeer/clearinghouse`
   - **Workflow filename:** `release.yml` (exact name, including `.yml`)
   - **Environment:** leave empty unless you use a GitHub Environment
3. Remove any leftover `NPM_TOKEN` repo secret — it overrides OIDC and causes `EOTP`.
4. Disable or delete the trusted publisher / `release.yml` on `pymthouse/clearinghouse`
   so only one repo can publish.

`package.json` `repository.url` must be `https://github.com/livepeer/clearinghouse.git`
(provenance / sigstore).

### Workflow requirements (already in `release.yml`)

- `permissions.id-token: write`
- `actions/setup-node` with `registry-url: https://registry.npmjs.org` and `scope: "@pymthouse"`
- **No** `NODE_AUTH_TOKEN` / `NPM_TOKEN` on the publish step
- npm CLI ≥ 11.5.1 (`npm publish`, not `pnpm publish`)

npm allows **one** trusted publisher workflow per package — stable tags and
feature-branch RCs share `release.yml`.

## Stable release

1. On `main` (or a release branch), run **Actions → bump-version** with `patch` /
   `minor` / `major`, **or** locally:

   ```bash
   cd identity-webhook
   npm version patch   # or minor / major
   git push origin HEAD --tags
   ```

2. The tag push (`v*.*.*`) starts **release**, which publishes to `latest` and
   creates a GitHub Release.

### Re-run a failed stable publish

**Actions → release → Run workflow** → mode `publish-tag` → tag `v0.4.2`.

## Feature-branch RC (manual)

No merge and no version commit required:

1. Push your feature branch to `livepeer/clearinghouse`.
2. **Actions → release → Run workflow**
   - **Use workflow from:** your feature branch
   - **mode:** `rc-from-branch`
   - **bump:** `patch` (default), or `minor` / `major`
3. CI resolves the next version from the registry (e.g. latest `0.4.2` →
   `0.4.3-rc.0`, then `0.4.3-rc.1`, …), publishes with dist-tag `rc`, and writes
   the version to the job summary.

Install:

```bash
npm i @pymthouse/clearinghouse-identity-webhook@rc
# or pin: npm i @pymthouse/clearinghouse-identity-webhook@0.4.3-rc.0
```

Detached RCs do **not** create git tags (avoids a second tag-triggered publish
against an un-bumped `package.json` on the branch).

## Local dry-run

```bash
cd identity-webhook
npm ci
npm test
npm pack --dry-run --ignore-scripts
```

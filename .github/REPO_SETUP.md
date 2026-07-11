# Making `livepeer/clearinghouse` match other Livepeer org repos

This is a checklist for bringing the empty `clearinghouse` repo up to the same
standard as established repos like `livepeer/go-livepeer` and `livepeer/dashboard`.

## 1. Community health files

GitHub recognizes these by location. Files in `.github/` apply repo-wide.

| File | Purpose | Status |
| --- | --- | --- |
| `.github/ISSUE_TEMPLATE/bug_report.yml` | Structured bug reports | ✅ added |
| `.github/ISSUE_TEMPLATE/feature_request.yml` | Structured feature requests | ✅ added |
| `.github/ISSUE_TEMPLATE/config.yml` | Disables blank issues, links Discussions/Discord/docs | ✅ added |
| `.github/PULL_REQUEST_TEMPLATE.md` | PR checklist | ✅ added |
| `README.md` | Overview, architecture, quickstart for both modes | ☐ to do |
| `LICENSE` | Match the org — go-livepeer uses **MIT** | ☐ to do |
| `CONTRIBUTING.md` | Contribution guide (can adapt go-livepeer's) | ☐ to do |
| `CODE_OF_CONDUCT.md` | Contributor Covenant (org standard) | ☐ to do |
| `CHANGELOG_PENDING.md` | Pending-release changelog — referenced in PR template | ☐ to do |
| `.gitignore` | Language-appropriate | ☐ to do |
| `SECURITY.md` | How to report vulnerabilities | ☐ optional |

> Tip: An org-level `.github` repo (`livepeer/.github`) can provide default
> community files. If it exists, repo-local files override it — so the templates
> here will take precedence where the clearinghouse needs project-specific fields.

## 2. Labels

The Livepeer convention uses prefixed labels. Create these so the templates'
auto-labels resolve:

- `type: bug`, `type: feature`, `type: documentation`, `type: enhancement`
- `need: triage`, `need: more info`
- `good first issue`, `help wanted`
- Component labels matching the SDK split: `area: builder-sdk`,
  `area: signer-proxy`, `area: usage-api`, `area: kafka`, `area: storage`,
  `area: docker`, `area: auth`

You can script this with the GitHub CLI:

```bash
gh label create "type: bug" --color d73a4a --repo livepeer/clearinghouse
gh label create "need: triage" --color fbca04 --repo livepeer/clearinghouse
# ...repeat per label
```

## 3. Milestones

You've already created milestones. Make sure each issue template encourages
assigning one, and that milestone names map to the build phases (e.g.
`builder-sdk core`, `signer proxy`, `usage tracking`, `hosted release`,
`on-prem release`).

## 4. Repo settings to match the org

- **Default branch**: `main` (dashboard uses `main`; go-livepeer is older and
  uses `master` — prefer `main` for a new repo).
- **Branch protection** on the default branch: require PR review, require status
  checks (CI) to pass, require linear history.
- **Enable Discussions** (the issue `config.yml` links to it).
- **Enable Issues** and **Projects** if you track work on a board.
- **Squash merge** for small changesets, **rebase and merge** for larger ones
  (matches the go-livepeer CONTRIBUTING guidance).
- Add a concise **repo description** and **topics** (`livepeer`, `sdk`,
  `usage-tracking`, `kafka`).

## 5. CI / automation (next layer)

Under `.github/workflows/`:

- `ci.yml` — unit tests and `npm pack --dry-run` for identity-webhook on PR.
- `release.yml` — tag-based npm publish via OIDC trusted publishing + GitHub Release.
- `bump-version.yml` — manual semver bump for identity-webhook.
- `dependabot.yml` — weekly npm + GitHub Actions updates for identity-webhook.

See [identity-webhook/docs/RELEASING.md](../identity-webhook/docs/RELEASING.md) for npm trusted publishing setup.

## How to apply

```bash
git clone https://github.com/livepeer/clearinghouse.git
cd clearinghouse
# copy the .github/ folder from this workspace into the repo root
git checkout -b chore/community-files
git add .github
git commit -m "chore: add issue/PR templates and community files"
git push origin chore/community-files
gh pr create
```

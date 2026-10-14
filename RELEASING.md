# Releasing infinimesh

One core tag releases core, inf-cli, platform-operator and the k8s Helm chart at the same version.

## Tags

- `vX.Y.Z` is a final release. Images get `vX.Y.Z` and `latest`, GitHub releases are marked latest, the Homebrew formula and every pinned default move to `vX.Y.Z`.
- `vX.Y.Z-rc.N` is a release candidate. Images get only that tag, every GitHub release is a pre-release, `latest`, the Homebrew formula and the pinned defaults stay where they are. The Helm chart is pushed as version `X.Y.Z-rc.N`, which Helm skips unless asked for.
- `vX.Y.Z-rN` is a core-only image revision and is not fanned out.

## Order

1. Tag core (`git tag vX.Y.Z && git push origin vX.Y.Z`). `scripts/check-stable.sh` runs in the release workflow before the GitHub release is published. A final tag fails when `CHANGELOG.md` says the release is for testing, or when a `github.com/infinimesh/` module is a pseudo-version, a pre-release, a `replace`, or a tag that does not exist. A release candidate skips that check.
2. `Docker Build` publishes `ghcr.io/infinimesh/core/<service>` and `Publish Release` creates the core GitHub release.
3. `Release orchestrator` runs after Docker Build succeeds. It waits for the core release, then sends `repository_dispatch` `core-release` with `{version, prerelease}` to inf-cli, platform-operator and k8s.
4. inf-cli `lockstep.yml` tags the same version on main. Its `release.yml` runs goreleaser and, on a final, merges the Homebrew formula PR.
5. platform-operator `lockstep.yml`: on a final it opens a PR moving `defaultCoreVersion`, the sample, `manifests/operator-image.yaml` and the README to the version, auto-merges it, then tags. On an rc it only tags. Its `release.yml` builds the operator image.
6. k8s `release.yml`: on a final it opens and auto-merges a PR setting `image.tag`, `appVersion` and the chart version, then tags, pushes the chart to `oci://ghcr.io/infinimesh/charts/infinimesh` and creates a release. On an rc it packages the chart from main with the rc version, pushes it, and creates a pre-release.
7. The orchestrator waits up to 45 minutes for a GitHub release of the same version in all three repos and fails if one is missing. On a final it then opens a PR moving core's own pins (`scripts/bump-pins.sh`).

Re-run a fan-out by hand: Actions, Release orchestrator, Run workflow, version `vX.Y.Z`.

## Token

The default `GITHUB_TOKEN` cannot trigger workflows in other repositories, and a tag pushed with it starts no workflow. The fan-out uses the `infinimesh-release` GitHub App through `actions/create-github-app-token`, with org secrets `RELEASE_APP_ID` and `RELEASE_APP_PRIVATE_KEY`. The App needs Contents, Pull requests, Actions and Packages read and write, and is installed on core, inf-cli, platform-operator and k8s. Without the secrets every lockstep job skips with a warning.

Auto-merge must be allowed in each repository (Settings, General, Allow auto-merge). Without it the bump PRs stay open and the job warns.

## Pins

`scripts/check-pins.sh` fails when a pinned core version is not a core tag or is older than the newest stable core release. It runs in core, inf-cli, platform-operator and k8s on every PR, on main and nightly through the reusable workflow `infinimesh/core/.github/workflows/check-pins.yml@main`. Right after a final release the check fails until the bump PRs are merged, which is the point.

Pinned files: core `.env.example`, `README.md` (Current version), `docs/production-on-kubernetes.md`; k8s `charts/infinimesh/values.yaml`, `Chart.yaml`, `README.md`; platform-operator `pkg/controller/platform/release.go`, `config/samples/`, `README.md`; website `llms-full.txt`. The website is not part of the fan-out, update it by hand.

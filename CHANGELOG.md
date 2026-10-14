# Changelog

All notable changes of `dlc-clinical-api` are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions follow
[Semantic Versioning](https://semver.org/). Work enters `develop` from `feat/`, `fix/` and `chore/` branches, reaches `qa` through
`promotion/*` branches re-applied with `git cherry-pick -x`, and reaches `main` through a `release/<version>` branch cut from `main`
and filled with the commits of `qa`, also with `git cherry-pick -x`, merged by PR with the approval of @ariel5253. A release is
tagged `v<version>` after the merge. Nothing is merged between permanent branches.

## [Unreleased]

MVP 2: the repository of the Clinical API (Go, hexagonal) is prepared. No operation of `clinical-service.yaml` is implemented on
`develop` yet, and there is no release on `main`.

### Added

- Repository documentation aligned with the DiLucca conventions.
  ([#1](https://github.com/code-corhuila/dlc-clinical-api/pull/1))

### Known limits

- No operation of `clinical-service.yaml` is implemented yet: no endpoints, `Idempotency-Key`, `expectedVersion`, `Page<T>`, error
  envelope, outbox events, JWT RS256, healthchecks, Dockerfile or CI. The bootstrap PR
  ([#2](https://github.com/code-corhuila/dlc-clinical-api/pull/2)) was closed without being merged.
- Depends on `dlc-clinical-db` for persistence and on Appointments, Billing and Workflow for care closure. Not yet verified against
  `dlc-front` or `dlc-clinical-portal`.

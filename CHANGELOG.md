# Changelog

All notable changes of `dlc-clinical-api` are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions follow
[Semantic Versioning](https://semver.org/). Work enters `develop` from `feat/`, `fix/` and `chore/` branches, reaches `qa` through
`promotion/*` branches re-applied with `git cherry-pick -x`, and reaches `main` through a `release/<version>` branch cut from `main`
and filled with the commits of `qa`, also with `git cherry-pick -x`, merged by PR with the approval of @ariel5253. A release is
tagged `v<version>` after the merge. Nothing is merged between permanent branches.

## [Unreleased]

MVP 3: the Clinical API implements every operation of `clinical-service.yaml` for HU-CLN-001, HU-CLN-002 and HU-XCT-001, with
the ADR-014/ADR-015 closure model and the ADR-017 write protocol, and the HU-CLN-003 analytics dashboard (ADR-006). It runs
as five processes from one image.

### Added

- Service skeleton (Go 1.26, hexagonal), explicit server limits, `/health` and `/health/ready`, single error envelope and
  `X-Correlation-Id`. ([#10](https://github.com/code-corhuila/dlc-clinical-api/pull/10),
  [#14](https://github.com/code-corhuila/dlc-clinical-api/pull/14), [#35](https://github.com/code-corhuila/dlc-clinical-api/pull/35))
- RS256 JWT verified by the service ([#15](https://github.com/code-corhuila/dlc-clinical-api/pull/15)) and session revocation
  checked with auth-service, required in qa and main ([#41](https://github.com/code-corhuila/dlc-clinical-api/pull/41)).
- Clinical records, entries (consultation, diagnosis, evolution, antecedent, allergy, amendments), treatments and procedure
  completion with justified extras: domain, use cases, routes and MongoDB persistence with `Idempotency-Key`, `expectedVersion`
  and `Page<T>` (#16–#28). Lists are ordered `createdAt` then `id` ascending
  ([#44](https://github.com/code-corhuila/dlc-clinical-api/pull/44)).
- Care-completions declaration, one closure per encounter, closure reads and retries (ADR-014/015)
  ([#29](https://github.com/code-corhuila/dlc-clinical-api/pull/29)–[#32](https://github.com/code-corhuila/dlc-clinical-api/pull/32)),
  and the rejection of writes to a declared encounter ([#34](https://github.com/code-corhuila/dlc-clinical-api/pull/34)).
- Transactional outbox published to RabbitMQ by `clinical-outbox-relay`, and `AppointmentCompleted`/`EncounterChargesCompleted`
  consumed through the inbox by `clinical-inbox-consumer` (#33, #36–#39).
- Owner contracts of `internal-coordination.yaml`: patient validation, dentist assignment and assigned-patient list, encounter
  status, the patient gate and the Billing manual-charge freeze; every owner fails closed (503) when it is not configured or
  cannot answer (#40, #45–#47).
- ADR-017 write protocol: write intents and encounter admissions, writes of patient clinical data under the activity guard,
  encounter admission and freeze around the declaration, intent fencing, and `clinical-intent-reconciler` for outcomes left
  unknown by a lost answer or a crash (#48–#54).
- MongoDB reached through the `clinical-data` network as `clinical_runtime` ([#56](https://github.com/code-corhuila/dlc-clinical-api/pull/56)).
- HU-CLN-003: projections from `Appointment*`, `PaymentRecorded` and `ClinicalCareCompleted` consumed by
  `clinical-analytics-consumer` on its own `clinical.analytics` queue, stored in MongoDB with exact COP, and the role-scoped
  `GET /api/v1/clinical-analytics/dashboard` in `CLINIC_TIMEZONE` ([#60](https://github.com/code-corhuila/dlc-clinical-api/pull/60)–[#64](https://github.com/code-corhuila/dlc-clinical-api/pull/64)).

### Fixed

- Each owner gets its own service credential (`<OWNER>_SERVICE_TOKEN_FILE`, required in qa and main) read on every
  call, so a short-lived service JWT with that owner's audience can be rotated in place; the shared `SERVICE_TOKEN` is for
  dev. An unreadable credential fails closed without calling the owner.
- The permission names that give an Administrator clinical access are configuration (`CLINICAL_READ_PERMISSION`,
  `CLINICAL_WRITE_PERMISSION`, default `clinical:read`/`clinical:write`) resolved by the HTTP adapter; the use cases
  receive booleans. Auth has not published its permission catalogue yet: these must match the names it issues.
- Access tokens are verified with the Auth JWKS by `kid` (`JWT_JWKS_URL`, required in qa and main), so a key rotation
  needs no restart; an unknown `kid` refreshes the set at most every `JWKS_MIN_REFRESH`, and an unreachable JWKS answers
  503. Tokens without `iat`, `jti`, `sid`, `roles` or `permissions` are rejected (authentication.md).
- Outside `dev` every owner URL is required, and every process refuses a database below the `schema_info` version it
  requires, instead of starting healthy and answering 503 or accepting duplicates.
- `scripts/integration-test.sh` runs the MongoDB and RabbitMQ integration tests against disposable, migrated instances;
  plain `go test ./...` skips them.
- Failing events are dead-lettered after the delivery limit on RabbitMQ 4 ([#58](https://github.com/code-corhuila/dlc-clinical-api/pull/58)).
- The image builds the analytics consumer again ([#65](https://github.com/code-corhuila/dlc-clinical-api/pull/65)).
- An unroutable event is retried with backoff instead of failing at once, so deploying Clinical before its consumers no longer
  loses events ([#66](https://github.com/code-corhuila/dlc-clinical-api/pull/66)).
- Procedure completion checks `expectedVersion` against the Treatment version, the only version `clinical-service.yaml`
  exposes, so a client that reads the treatment can complete its procedures.

### Known limits

- Verified end to end with `dlc-infra-mongo`, `dlc-clinical-db`, RabbitMQ 4, `dlc-clinical-portal` and `dlc-front`, but with
  fake owner contracts and a local stand-in for `dlc-api-gateway`; not yet verified against the real Auth, Patients,
  Appointments and Billing services.

## [2.0.0] - 2026-10-14

MVP 2: the repository of the Clinical API (Go, hexagonal) is prepared. No operation of `clinical-service.yaml` is implemented
in this release. Released through `release/2.0.0`, tagged `v2.0.0` after the merge.

### Added

- Repository documentation aligned with the DiLucca conventions.
  ([#1](https://github.com/code-corhuila/dlc-clinical-api/pull/1))

### Known limits

- No operation of `clinical-service.yaml` is implemented yet: no endpoints, `Idempotency-Key`, `expectedVersion`, `Page<T>`, error
  envelope, outbox events, JWT RS256, healthchecks, Dockerfile or CI. The bootstrap PR
  ([#2](https://github.com/code-corhuila/dlc-clinical-api/pull/2)) was closed without being merged.
- Depends on `dlc-clinical-db` for persistence and on Appointments, Billing and Workflow for care closure. Not yet verified against
  `dlc-front` or `dlc-clinical-portal`.

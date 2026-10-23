# Changelog

All notable changes of `dlc-clinical-api` are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions follow
[Semantic Versioning](https://semver.org/). Work enters `develop` from `feat/`, `fix/` and `chore/` branches, reaches `qa` through
`promotion/*` branches re-applied with `git cherry-pick -x`, and reaches `main` through a `release/<version>` branch cut from `main`
and filled with the commits of `qa`, also with `git cherry-pick -x`, merged by PR with the approval of @ariel5253. A release is
tagged `v<version>` after the merge. Nothing is merged between permanent branches.

## [Unreleased]

MVP 3: the Clinical API implements every operation of `clinical-service.yaml` for HU-CLN-001, HU-CLN-002 and HU-XCT-001, with
the ADR-014/ADR-015 closure model and the ADR-017 write protocol. It runs as four processes from one image.

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

### Known limits

- HU-CLN-003 is not implemented: its indicators are not approved yet.
- Verified with fake owner contracts and a MongoDB 8.0 replica set; not yet verified against the real Auth, Patients,
  Appointments and Billing services, nor against `dlc-clinical-portal`, whose `completeProcedure` still expects a `CareClosure`
  instead of the ADR-014 `ProcedureCompletion`.
- MongoDB provisioning, identities, TLS and backups belong to `dlc-infra-mongo` and are still pending there.

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

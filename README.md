# dlc-clinical-api

> Clinical bounded context: Go service API.

This repository exposes the **Di Lucca Clinical** application boundary. It implements clinical
use cases and publishes the contracts that other bounded contexts may consume; it never exposes
the Clinical database as an integration mechanism.

## Scope

Clinical owns histories, antecedents, allergies, consultations, diagnoses, treatments,
procedures, clinical evolution, the care-completions declaration and its closure, and the
Clinical outbox events (`ProcedureCompleted`, `ClinicalCareCompleted` and the others of the
contract). It does not own
administrative patient data, appointment scheduling, billing/money, IAM, sessions or the
cross-domain saga/workflow.

## Technical baseline

- Go 1.26 and the official MongoDB Go driver.
- MongoDB 8 through the Clinical database artifact; direct database access is exclusive to this
  service.
- REST contracts documented as OpenAPI and protected with the platform JWT boundary.
- Idempotency and durable outbox handling where the Clinical contract requires them.

Keep dependency and event payloads limited to the published contracts. No service reads another
bounded context's database. Schema changes, validators, migrations and development seeds belong
exclusively in [`dlc-clinical-db`](https://github.com/code-corhuila/dlc-clinical-db), never in
this API repository.

## Run locally

Requires Go 1.26 and a MongoDB replica set migrated by `dlc-clinical-db`. Variables are listed in
[`.env.example`](.env.example); every limit is declared there and zero values are rejected because
they mean "no limit". Each owner contract (`AUTH_`, `PATIENTS_`, `APPOINTMENTS_`,
`BILLING_INTERNAL_URL`) is optional in `dev`; an owner left empty answers 503, so its
operations fail closed instead of trusting an unverified state.

```bash
cp .env.example .env
go run ./cmd/clinical-api
curl http://localhost:8080/health   # {"status":"UP"}, no token required
```

Container, attached to the external `platform` network provided by the infrastructure:

```bash
docker compose -f deploy/compose.yml up --build
```

## Layout (Anexo C, Go)

```text
cmd/clinical-api/            HTTP API composition root and explicit limits
cmd/clinical-outbox-relay/   publishes committed outbox events to RabbitMQ
cmd/clinical-inbox-consumer/ applies closure outcome events through the inbox
cmd/clinical-intent-reconciler/ settles ADR-017 write intents left PENDING
internal/config/             validated configuration
internal/domain/model/       entities, value objects, invariants (no framework imports)
internal/application/        inbound/outbound ports and one use case per operation
internal/adapter/in/httpapi/ routes, validation, error envelope, correlation, JWT
internal/adapter/out/        MongoDB persistence, messaging and owner contracts (/internal/v1)
deploy/                      Dockerfile and compose (no database, no volume)
```

## Checks

```bash
gofmt -l .
go vet ./...
go test ./...
```

## Documentation

The authoritative specifications and governance live in
[`dlc-docs`](https://github.com/code-corhuila/dlc-docs). Read the Clinical scope, the service
catalog, integration rules and repository/PR regulations before implementing a use case. The
published Clinical contracts define the operations that require idempotency and outbox handling.

## Branching

Three permanent branches. **None of them accepts a direct commit** — you enter through a child
branch and leave through a Pull Request.

```
develop  <--PR--  feat/... fix/... chore/...
qa       <--PR--  qa/...
main     <--PR--  release/... hotfix/...
```

Promotion happens **by re-application** (`git cherry-pick -x`), never by merging one permanent
branch into another: `merge develop -> qa` and `merge qa -> main` do not exist in this model.

`main` approval is enforced through the repository's `CODEOWNERS`. Review rules for `develop`
and `qa` are defined by the team according to the course regulation.

Every Pull Request declares the affected user story (or why it is not applicable), stays within
the permitted diff size and targets the correct permanent branch.

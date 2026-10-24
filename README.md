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
operations fail closed instead of trusting an unverified state. In `qa` and `main` all four are
required and the process refuses to start without them.

Every process also refuses to start when the database `schema_info` version is below the one it
requires (`RequiredSchemaVersion`): run the `dlc-clinical-db` migrations first.

```bash
cp .env.example .env
go run ./cmd/clinical-api
curl http://localhost:8080/health   # {"status":"UP"}, no token required
```

## Deploy

One image, five processes (`deploy/compose.yml`): `clinical-api`, `clinical-outbox-relay`,
`clinical-inbox-consumer`, `clinical-intent-reconciler` and `clinical-analytics-consumer`. In order:

1. `dlc-infra-mongo`: replica set, authentication and the `clinical-data` network.
2. `dlc-clinical-db`: migrations (they create the runtime roles and `schema_info`).
3. `dlc-infra-mongo`: the `clinical_runtime` identity, which needs the roles from step 2.
4. This repository, with `.env` from `.env.example` and the secret files in `CLINICAL_SECRETS_DIR`
   (mounted read-only at `/run/secrets/clinical`):

```bash
CLINICAL_SECRETS_DIR=/srv/clinical/secrets docker compose -f deploy/compose.yml up --build -d
```

Every process restarts until its dependencies are ready: it refuses a database below
`RequiredSchemaVersion` and, in `qa`/`main`, a configuration without every owner. `clinical-api`
reports readiness through `clinical-api healthcheck` (`GET /health/ready`, MongoDB reachable).
RabbitMQ is reached through `RABBITMQ_URL` on the `platform` network.

### Integration checklist

What Clinical needs from each owner to run in `qa`/`main`; everything else is already in place.

| Owner | Provides | Clinical setting |
|---|---|---|
| Auth (`auth-service`) | Public JWKS at `/api/v1/auth/jwks`; access tokens with `iat`, `jti`, `sid`, `roles`, `permissions`; `POST /internal/v1/sessions/validations`; the permission names that grant an Administrator clinical access | `JWT_JWKS_URL`, `JWT_ISSUER`, `JWT_AUDIENCE`, `AUTH_INTERNAL_URL`, `AUTH_SERVICE_TOKEN_FILE`, `CLINICAL_READ_PERMISSION`, `CLINICAL_WRITE_PERMISSION` |
| Patients | `/internal/v1/patients/{id}/validation` | `PATIENTS_INTERNAL_URL`, `PATIENTS_SERVICE_TOKEN_FILE` |
| Appointments | Assignments, assigned-patient list, encounter status and the patient gate of `internal-coordination.yaml`; `Appointment*` events | `APPOINTMENTS_INTERNAL_URL`, `APPOINTMENTS_SERVICE_TOKEN_FILE` |
| Billing | Manual-charge freeze and release; `EncounterChargesCompleted` and `PaymentRecorded` events | `BILLING_INTERNAL_URL`, `BILLING_SERVICE_TOKEN_FILE` |
| Service identity | One short-lived service JWT per owner audience, written to its file (read on every call) | `*_SERVICE_TOKEN_FILE` |
| Gateway | Routes `/api/v1/clinical-records`, `/clinical-entries`, `/treatments`, `/procedures`, `/care-closures` and `/clinical-analytics` to `clinical-api:8080`, keeping the `/api/v1` prefix | — |
| RabbitMQ | A broker and credentials; Clinical declares `clinical.events`, the exchanges it reads and its own queues idempotently | `RABBITMQ_URL` |

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
go test ./...                                # unit tests; integration tests are skipped
scripts/integration-test.sh ../dlc-clinical-db # also the MongoDB and RabbitMQ integration tests
```

`scripts/integration-test.sh` starts a disposable MongoDB 8 replica set migrated by
`dlc-clinical-db` and a RabbitMQ 4 broker, runs `go test ./...` with `TEST_MONGO_URI` and
`TEST_RABBITMQ_URL`, and removes them; extra arguments go to `go test` (for example `-v`). It needs
Docker and a checkout of `dlc-clinical-db`.

Coverage baseline (DoD: it must not decrease): **91.1%** of `internal/` with the integration tests,
measured against a MongoDB replica set migrated by `dlc-clinical-db` and a RabbitMQ broker. The
`cmd/` composition roots are excluded; without `TEST_MONGO_URI` and `TEST_RABBITMQ_URL` the
integration tests are skipped and the figure is lower.

```bash
TEST_MONGO_URI=... TEST_RABBITMQ_URL=... go test -p 1 -coverpkg=./internal/... -coverprofile=cover.out ./internal/...
go tool cover -func=cover.out | tail -1
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

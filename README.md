# dlc-clinical-api

> Clinical bounded context: Go service API.

This repository exposes the **Di Lucca Clinical** application boundary. It implements clinical
use cases and publishes the contracts that other bounded contexts may consume; it never exposes
the Clinical database as an integration mechanism.

## Scope

Clinical owns histories, antecedents, allergies, consultations, diagnoses, treatments,
procedures, clinical evolution and the `ProcedureCompleted` outbox event. It does not own
administrative patient data, appointment scheduling, billing/money, IAM, sessions or the
cross-domain saga/workflow.

## Technical baseline

- Go 1.26 and the official MongoDB Go driver.
- MongoDB 8 through the Clinical database artifact; direct database access is exclusive to this
  service.
- REST contracts documented as OpenAPI and protected with the platform JWT boundary.
- Idempotency and durable outbox handling where the Clinical contract requires them.

## Bootstrap layout and local setup

This repository starts from the Go layout required by the course API annex:

```text
cmd/clinical-api/                 composition root and process lifecycle
internal/domain/model/            entities, value objects and invariants; no infrastructure imports
internal/application/port/in/     use-case interfaces and commands/queries
internal/application/port/out/    repository, clock, ID, authorization and outbox ports
internal/application/usecase/     use-case implementations
internal/adapter/in/httpapi/      HTTP routes, auth, correlation and error translation
internal/adapter/out/persistence/ MongoDB adapters only; never migrations
internal/config/                  runtime configuration read at the composition root
deploy/                           container build and local API composition
```

The bootstrap intentionally exposes only unauthenticated `GET /health`. Clinical routes are
added contract-first through input ports, then use cases, and finally HTTP/Mongo adapters. A
handler must never access MongoDB directly, and domain/application packages must not import an
HTTP framework, the MongoDB driver or environment configuration.

### Local prerequisites

- Go 1.26.
- Docker Desktop only when running the containerized API or the separately owned Clinical MongoDB
  artifact.
- A local copy of the variables in `.env.example` as `.env`; never commit it.

```powershell
Copy-Item .env.example .env
go test ./...
go vet ./...
docker compose -f deploy/compose.yml up --build
```

`dlc-clinical-db` owns MongoDB, Liquibase, validators, indexes, roles and development data. This
API repository only consumes its connection settings through configuration once the persistence
adapter is implemented; it never runs migrations.

`deploy/compose.yml` joins the external `platform` network created by `dlc-infra` and exposes
port 8080 only to that network. It deliberately does not publish a host port: external traffic
must enter through `dlc-api-gateway`. The container runs as a non-root user with a read-only root
filesystem, dropped Linux capabilities, a writable `/tmp` only, and a `/health` healthcheck.
For an isolated container verification, build it and inspect its health; do not publish it as a
replacement for the gateway path.

### Runtime conventions

- `GET /health` is public. All future Clinical routes validate RS256 JWTs in this service, even
  when the gateway has already checked a token.
- `X-Correlation-Id` is accepted or generated and returned in every current HTTP response.
- The composition root declares the initial server limits: 5 s header read, 15 s write, 60 s idle
  and 20 s graceful shutdown.
- The durable idempotency/outbox implementation is deferred until the published contract and
  database migration changes are approved. It must be modelled as outbound ports and implemented
  atomically with Clinical writes; no in-memory production substitute is permitted.

Keep dependency and event payloads limited to the published contracts. No service reads another
bounded context's database. Schema changes, validators, migrations and development seeds belong
exclusively in [`dlc-clinical-db`](https://github.com/code-corhuila/dlc-clinical-db), never in
this API repository.

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

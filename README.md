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

Keep dependency and event payloads limited to the published contracts. No service reads another
bounded context's database.

## Documentation

The authoritative specifications and governance live in
[`dlc-docs`](https://github.com/code-corhuila/dlc-docs). Read the Clinical scope, the service
catalog, integration rules and repository/PR regulations before implementing a use case.

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

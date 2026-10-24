## User story

code-corhuila/dlc-docs#<NN> — HU-CLN-<NNN> (or why it does not apply)

## What changes and why

-

## How it was tested

- Tests covering the change:
- `ci.yml` result:

## Promotion trail (only for `qa` or `main`)

- `<sha>` (cherry picked from commit `<sha>`)

## Checklist

- [ ] No secrets, `.env`, keys or tokens committed
- [ ] No schema, validator, index or migration changes (they belong to `dlc-clinical-db`)
- [ ] Contract respected: `clinical-service.yaml` and `_shared.yaml`
- [ ] Diff within 400 changed lines, excluding tests and generated files
- [ ] Domain imports no web framework, database driver or messaging library

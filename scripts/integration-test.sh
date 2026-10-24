#!/usr/bin/env bash
# Runs every integration test against disposable MongoDB 8 (replica set, schema
# migrated by dlc-clinical-db) and RabbitMQ 4. Plain `go test ./...` skips them.
# Usage: scripts/integration-test.sh [path to dlc-clinical-db, default ../dlc-clinical-db]
set -euo pipefail

readonly db_repo="${1:-${DLC_CLINICAL_DB_DIR:-../dlc-clinical-db}}"
readonly mongo_image='mongo@sha256:4968f22d0c6c10ef29952f3e807f62872ba22b3312f25803564fbfc08255efc2'
readonly rabbit_image='rabbitmq:4-management-alpine'
readonly run_id="it-$$"
readonly network="clinical-${run_id}"
readonly mongo="clinical-mongo-${run_id}"
readonly rabbit="clinical-rabbit-${run_id}"

test -f "$db_repo/deploy/compose.yml" || { echo "dlc-clinical-db not found at $db_repo" >&2; exit 2; }

cleanup() {
  docker rm -f "$mongo" "$rabbit" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$network" >/dev/null
docker run -d --name "$mongo" --network "$network" -p 127.0.0.1::27017 "$mongo_image" mongod --replSet rs0 --bind_ip_all >/dev/null
docker run -d --name "$rabbit" --network "$network" -p 127.0.0.1::5672 "$rabbit_image" >/dev/null

for _ in $(seq 1 30); do
  docker exec "$mongo" mongosh --quiet --eval 'db.adminCommand({ping:1}).ok' 2>/dev/null | grep -qx 1 && break
  sleep 1
done
# The member advertises its container name for the migrator; the tests connect directly through the published port.
docker exec "$mongo" mongosh --quiet --eval "rs.initiate({_id:'rs0',members:[{_id:0,host:'$mongo:27017'}]})" >/dev/null
for _ in $(seq 1 30); do
  docker exec "$mongo" mongosh --quiet --eval 'db.hello().isWritablePrimary ? 1 : 0' | grep -qx 1 && break
  sleep 1
done

# Migrations reach the member by container name on the shared network.
(cd "$db_repo" && MONGO_HOST="$mongo" PLATFORM_NETWORK_NAME="$network" \
  docker compose -f deploy/compose.yml --profile tooling run --rm clinical-db-migrate update >/dev/null)

mongo_port=$(docker port "$mongo" 27017/tcp | head -1 | sed 's/.*://')
rabbit_port=$(docker port "$rabbit" 5672/tcp | head -1 | sed 's/.*://')
for _ in $(seq 1 60); do
  docker exec "$rabbit" rabbitmq-diagnostics -q check_port_connectivity >/dev/null 2>&1 && break
  sleep 2
done

TEST_MONGO_URI="mongodb://127.0.0.1:${mongo_port}/?directConnection=true" \
TEST_RABBITMQ_URL="amqp://guest:guest@127.0.0.1:${rabbit_port}/" \
  go test -count=1 ./... "${@:2}"

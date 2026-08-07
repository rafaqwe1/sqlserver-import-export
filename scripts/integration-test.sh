#!/usr/bin/env bash
# Manages the dedicated SQL Server container used by the integration test
# suite (internal/integration, build tag "integration") and optionally runs
# it. This container is separate from any other SQL Server container on the
# machine (different name, different port) so it's always safe to start,
# stop, or wipe.
set -euo pipefail

CONTAINER_NAME="sqlserver-import-export-test"
HOST_PORT="14330"
SA_PASSWORD="TestPass123!"
IMAGE="liaisonintl/mssql-server-linux:v2019"

up() {
	if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
		if [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME")" != "true" ]; then
			docker start "$CONTAINER_NAME" >/dev/null
		fi
	else
		docker run -d --name "$CONTAINER_NAME" \
			-e ACCEPT_EULA=Y \
			-e SA_PASSWORD="$SA_PASSWORD" \
			-p "${HOST_PORT}:1433" \
			"$IMAGE" >/dev/null
	fi

	echo "Waiting for SQL Server to accept connections on localhost:${HOST_PORT}..."
	for _ in $(seq 1 30); do
		if docker exec "$CONTAINER_NAME" /opt/mssql-tools/bin/sqlcmd \
			-S localhost -U sa -P "$SA_PASSWORD" -Q "SELECT 1" >/dev/null 2>&1; then
			echo "Ready."
			return 0
		fi
		sleep 3
	done
	echo "Timed out waiting for SQL Server to become ready" >&2
	exit 1
}

down() {
	docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
	echo "Removed $CONTAINER_NAME"
}

run_tests() {
	up
	SQLSERVER_TEST_HOST="localhost:${HOST_PORT}" \
		SQLSERVER_TEST_USER="sa" \
		SQLSERVER_TEST_PASSWORD="$SA_PASSWORD" \
		go test -tags integration ./internal/integration/... "$@"
}

case "${1:-test}" in
	up) up ;;
	down) down ;;
	test) shift || true; run_tests "$@" ;;
	*)
		echo "usage: $0 [up|down|test] [go test flags...]" >&2
		exit 2
		;;
esac

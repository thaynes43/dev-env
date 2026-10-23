#!/usr/bin/env bash
# Hosted disposable service only. No cluster API, host network, images built,
# production addresses, secrets, or arbitrary command interface.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true ]]
[[ ${PORTAL_POSTGRES_CONTAINER:-} =~ ^[a-f0-9]{64}$ ]]
case ${1:-} in
  snapshot)
    docker exec -u postgres -e PGPASSWORD=synthetic-ci-admin-only "$PORTAL_POSTGRES_CONTAINER" \
      pg_basebackup -h 127.0.0.1 -U postgres -D /tmp/portal-recovery-standby -X stream -R -c fast >/dev/null 2>&1
    docker exec -u postgres "$PORTAL_POSTGRES_CONTAINER" \
      pg_ctl -D /tmp/portal-recovery-standby -l /tmp/portal-standby.log -o '-p 5433' -w start >/dev/null 2>&1
    ;;
  partition)
    # Stop the disposable receiver BEFORE DELETE. No future WAL can be received
    # or replayed; replay_pause alone would allow promotion to apply the DELETE.
    docker exec -u postgres "$PORTAL_POSTGRES_CONTAINER" \
      pg_ctl -D /tmp/portal-recovery-standby -m fast -w stop >/dev/null 2>&1
    ;;
  promote)
    docker exec -u postgres "$PORTAL_POSTGRES_CONTAINER" \
      pg_ctl -D /tmp/portal-recovery-standby -l /tmp/portal-standby.log \
      -o "-p 5433 -c primary_conninfo=''" -w start >/dev/null 2>&1
    docker exec -u postgres "$PORTAL_POSTGRES_CONTAINER" \
      pg_ctl -D /tmp/portal-recovery-standby -w promote >/dev/null 2>&1
    ;;
  cleanup)
    docker exec -u postgres "$PORTAL_POSTGRES_CONTAINER" \
      pg_ctl -D /tmp/portal-recovery-standby -m fast -w stop >/dev/null 2>&1 || true
    ;;
  *) exit 64 ;;
esac

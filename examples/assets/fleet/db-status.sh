#!/bin/sh
# Publish whether the database answers. Delivered verbatim by `file.copy`: it is a script,
# not a template — every value it needs arrives through EnvironmentFile, so nothing here is
# substituted on the control host.
set -eu

if psql -tAc 'SELECT 1' >/dev/null 2>&1; then
    printf 'database=up host=%s\n' "$PGHOST" > "$STATUS_FILE"
else
    printf 'database=down host=%s\n' "$PGHOST" > "$STATUS_FILE"
    exit 1
fi

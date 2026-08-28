#!/bin/sh
# Nightly backup, run by app-backup.timer. Deliberately boring: the plan's job is to make
# sure it exists, is executable, and runs on a schedule.
set -e
stamp="$(date +%Y%m%d)"
tar czf "/var/backups/app/app-$stamp.tar.gz" -C /opt/hosting/app .env
find /var/backups/app -name 'app-*.tar.gz' -mtime +30 -delete

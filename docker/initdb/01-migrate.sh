#!/bin/sh
# Runs once, when the postgres container initializes an empty volume.
# Applies every up migration in order; 02-seed.sql runs after this.
#
# ponytail: no migration tracking; after adding a migration, recreate the
# volume (docker compose down -v) or apply the new file by hand.
set -e
for f in /migrations/*.up.sql; do
  echo "applying $f"
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -f "$f"
done

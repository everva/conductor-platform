-- DB-name-agnostic extension init (mirrors optiway qa/infra/qa-postgres-init.sql).
-- Runs against POSTGRES_DB and sets search_path on the current database, so it
-- works for any db name (here: optiway_qa).
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "postgis";
CREATE SCHEMA IF NOT EXISTS extensions;
CREATE EXTENSION IF NOT EXISTS "vector" SCHEMA extensions;

DO $$
BEGIN
  EXECUTE format('ALTER DATABASE %I SET search_path TO %s',
                 current_database(), '"$user", public, extensions');
END
$$;

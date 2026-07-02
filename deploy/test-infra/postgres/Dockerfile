# Optiway test-infra postgres: PostGIS 16 + pgvector (mirrors optiway infra/docker/postgres).
# pgvector via apt avoids a source build; PostGIS + uuid-ossp + vector extensions are
# created at init by init-extensions.sql.
FROM imresamu/postgis:16-3.4

RUN apt-get update \
    && apt-get install -y --no-install-recommends postgresql-16-pgvector \
    && rm -rf /var/lib/apt/lists/*

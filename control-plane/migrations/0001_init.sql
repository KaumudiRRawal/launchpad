-- Core Launchpad schema: accounts own projects, projects have services and
-- environments, and a deployment is one service built and released into one
-- environment.

CREATE TABLE accounts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email      TEXT        NOT NULL UNIQUE,
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE projects (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     UUID        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    slug           TEXT        NOT NULL,
    name           TEXT        NOT NULL,
    repo_url       TEXT        NOT NULL,
    default_branch TEXT        NOT NULL DEFAULT 'main',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, slug)
);

CREATE INDEX projects_account_id_idx ON projects (account_id);

-- A service is one deployable unit inside a repo. Monorepos have several,
-- distinguished by source_path.
CREATE TABLE services (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    source_path TEXT        NOT NULL DEFAULT '.',
    port        INTEGER     NOT NULL DEFAULT 8080 CHECK (port BETWEEN 1 AND 65535),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX services_project_id_idx ON services (project_id);

-- Environments are the isolation boundary. Every deployment, database and
-- subdomain belongs to exactly one, so nothing crosses between preview and
-- production.
CREATE TYPE environment_kind AS ENUM ('preview', 'production');

CREATE TABLE environments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID             NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind       environment_kind NOT NULL,
    name       TEXT             NOT NULL,
    subdomain  TEXT             NOT NULL UNIQUE,
    created_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX environments_project_id_idx ON environments (project_id);

-- Exactly one production environment per project; previews are unbounded.
CREATE UNIQUE INDEX environments_one_production_per_project_idx
    ON environments (project_id)
    WHERE kind = 'production';

CREATE TABLE provisioned_databases (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id UUID        NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    engine         TEXT        NOT NULL DEFAULT 'postgres',
    database_name  TEXT        NOT NULL,
    dsn_secret_ref TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (environment_id, database_name)
);

CREATE TYPE deployment_status AS ENUM (
    'queued', 'building', 'deploying', 'live', 'failed', 'superseded'
);

CREATE TABLE deployments (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id     UUID              NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    environment_id UUID              NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    commit_sha     TEXT              NOT NULL,
    status         deployment_status NOT NULL DEFAULT 'queued',
    image_ref      TEXT,
    url            TEXT,
    error_message  TEXT,
    queued_at      TIMESTAMPTZ       NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ       NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ       NOT NULL DEFAULT now()
);

CREATE INDEX deployments_service_id_idx ON deployments (service_id);
CREATE INDEX deployments_environment_id_idx ON deployments (environment_id);

-- The deploy worker polls for pending work; keep that scan off the main table.
CREATE INDEX deployments_pending_idx
    ON deployments (queued_at)
    WHERE status IN ('queued', 'building', 'deploying');

-- Build logs stream in line by line while a deployment runs.
CREATE TABLE deployment_logs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_id UUID        NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    seq           INTEGER     NOT NULL,
    stream        TEXT        NOT NULL DEFAULT 'stdout',
    message       TEXT        NOT NULL,
    logged_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (deployment_id, seq)
);

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER accounts_set_updated_at BEFORE UPDATE ON accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER projects_set_updated_at BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER services_set_updated_at BEFORE UPDATE ON services
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER environments_set_updated_at BEFORE UPDATE ON environments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER provisioned_databases_set_updated_at BEFORE UPDATE ON provisioned_databases
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER deployments_set_updated_at BEFORE UPDATE ON deployments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

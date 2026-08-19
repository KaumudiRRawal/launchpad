-- A deployment now records two addresses.
--
-- `url` is what a user visits: the environment's own subdomain, served by the
-- platform proxy. `internal_url` is where the workload actually listens, which
-- belongs to whichever driver released it and changes every time a container
-- is replaced. Separating them is what lets the public address of an
-- environment stay stable across deployments.
ALTER TABLE deployments ADD COLUMN internal_url TEXT;

-- The proxy resolves a hostname to an upstream on every request it cannot
-- serve from its cache, so that lookup gets its own index rather than scanning
-- an environment's whole deployment history.
CREATE INDEX deployments_live_by_environment_idx
    ON deployments (environment_id, completed_at DESC)
    WHERE status = 'live';

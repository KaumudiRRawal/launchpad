-- Latency and reliability are measured at the proxy, because every request to
-- a deployed workload already passes through it. Nothing has to be installed
-- in the deployed application, and a workload that stops answering is measured
-- by the same code path that measured it when it was healthy — an agent inside
-- the container would go quiet at exactly the moment the numbers matter.
--
-- Observations are rolled up per minute per deployment before they are stored.
-- One row per request would make the metrics table larger than every other
-- table combined within a day, and no question the analysis asks needs
-- individual requests.
CREATE TABLE deployment_metrics (
    deployment_id  UUID             NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    -- Denormalised from the deployment so a whole environment's traffic can be
    -- read without joining: the analysis always asks about an environment,
    -- never about one deployment, because a release boundary falls in the
    -- middle of the window it compares.
    environment_id UUID             NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    -- Truncated to the minute. The start of the interval, not the end.
    bucket         TIMESTAMPTZ      NOT NULL,
    requests       BIGINT           NOT NULL CHECK (requests > 0),
    -- A failure is a 5xx: the platform could not get a working answer out of
    -- the workload. A 404 the application chose to return is its own business.
    failures       BIGINT           NOT NULL CHECK (failures >= 0),
    latency_sum_ms DOUBLE PRECISION NOT NULL CHECK (latency_sum_ms >= 0),
    latency_max_ms DOUBLE PRECISION NOT NULL CHECK (latency_max_ms >= 0),
    -- The latency distribution, as counts against the fixed bucket boundaries
    -- in internal/metrics. Storing the distribution rather than a precomputed
    -- percentile is what lets minutes be combined correctly afterwards: the
    -- mean of two p95s is not the p95 of the two minutes together.
    histogram      BIGINT[]         NOT NULL CHECK (array_length(histogram, 1) > 0),
    PRIMARY KEY (deployment_id, bucket)
);

-- Every read is "one environment, recent first"; the primary key is no help
-- for that because it leads with the deployment.
CREATE INDEX deployment_metrics_environment_bucket_idx
    ON deployment_metrics (environment_id, bucket DESC);

-- Element-wise addition, so two flushes of the same minute accumulate instead
-- of one overwriting the other. Two control-plane replicas both proxying
-- traffic for the same environment each hold their own copy of that minute,
-- and whichever writes second must add to what it finds rather than replace
-- it.
CREATE FUNCTION histogram_add(a BIGINT[], b BIGINT[]) RETURNS BIGINT[]
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT array_agg(coalesce(x, 0) + coalesce(y, 0) ORDER BY n)
    FROM unnest(a, b) WITH ORDINALITY AS t (x, y, n)
$$;

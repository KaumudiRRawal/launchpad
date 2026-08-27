# Architecture

How Launchpad is put together, and why it is put together that way. The README
says what the platform does; this says what happens inside it and which
alternatives were considered and rejected.

## The shape of it

```
                    ┌────────────────────────┐
   Browser ─────────▶  Dashboard             │  React + TypeScript
                    │  types generated from  │  served by Vite in dev,
                    │  the OpenAPI document  │  static files in production
                    └───────────┬────────────┘
                                │ /v1, one origin, no CORS
                    ┌───────────▼────────────┐
                    │  Control plane :8080   │  Go
                    │  ─ REST API            │
                    │  ─ Deploy workers      │
                    │  ─ Analysis engine     │
                    └──┬──────────────────┬──┘
                       │                  │
              ┌────────▼──────┐   ┌───────▼─────────────┐
              │  PostgreSQL   │   │  Driver             │
              │  the only     │   │  ─ Docker (local)   │
              │  place state  │   │  ─ Cloud Run (prod) │
              │  is written   │   └───────┬─────────────┘
              └────────▲──────┘           │
                       │                  ▼
              ┌────────┴──────┐   ┌─────────────────────┐
  Public ─────▶  Proxy :8081  ├──▶│  Deployed workloads │
  traffic     │  routes by    │   └─────────────────────┘
              │  hostname,    │
              │  measures     │
              │  every request│
              └───────────────┘
```

One binary serves both listeners. The API and the proxy share the store, the
migrations and the configuration, and splitting them would produce two
deployables that have to be released together. On a runtime that publishes one
port per service the same image simply runs twice with the ports swapped — see
[terraform/README.md](../terraform/README.md).

The control plane is the only component that writes platform state. The proxy
reads upstreams and appends measurements; deployed workloads have no access to
the platform's database at all.

## The deployment path

A `POST /v1/deployments` inserts a row and returns. Nothing is built
synchronously: a build takes tens of seconds and an HTTP request that waits for
one is a request that times out behind a load balancer.

A worker then picks it up:

```
queued ──▶ building ──▶ deploying ──▶ live ──▶ superseded
   │           │            │
   └───────────┴────────────┴──▶ failed
```

1. **Claim.** `UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1)`
   takes the oldest queued row and moves it to `building` in one statement.
   Several workers can run without ever building the same deployment twice, and
   each takes a different row instead of blocking on the same one.

2. **Fetch.** An empty repository is initialised and the single requested commit
   is fetched at depth 1. A full clone spends minutes retrieving history the
   build will never read.

3. **Detect.** `Dockerfile` first, then `go.mod`, `package.json`,
   `requirements.txt`, `pyproject.toml`. A repository that described its own
   build gets that build; guessing instead would be both rude and wrong. The
   generated Dockerfiles are multi-stage and drop to uid 10001.

4. **Build.** The driver produces an image and reports the reference it actually
   produced, which is not always the one it was asked for.

5. **Release.** The workload name is derived from the environment's subdomain and
   is therefore stable, so releasing replaces the running workload rather than
   standing a second one beside it.

6. **Record.** The deployment is marked live with two addresses, the proxy's
   route cache for that subdomain is invalidated so traffic moves immediately,
   and the environment's previous deployments are marked superseded.

Every transition is an `UPDATE` whose `WHERE` clause names the status it expects
to find. Checking and writing are one atomic operation; verifying first and
updating second would leave a window for another worker to change the row in
between. `domain.DeploymentStatus.CanTransitionTo` is the same lifecycle in Go,
for callers that want to ask before trying.

Build output is buffered and flushed in batches of twenty-five. A verbose build
emits thousands of lines and one `INSERT` each would make the database the
slowest part of a deployment. Losing a log line never fails a deployment, and
the flush uses a background context so output still lands when a build is being
torn down after a timeout — which is exactly when the log matters most.

## Drivers

```go
type Driver interface {
	Build(ctx context.Context, req BuildRequest, logs LogWriter) (BuildResult, error)
	Release(ctx context.Context, req ReleaseRequest, logs LogWriter) (ReleaseResult, error)
	Name() string
}
```

Three methods, no configuration in the interface, and the engine knows nothing
else about where a deployment lands. `LAUNCHPAD_DEPLOY_DRIVER` chooses one at
boot.

Both drivers shell out to the vendor CLI rather than linking its client
libraries. The commands are stable, documented and identical to what a person
would run by hand, and the alternative is a large generated dependency tree to
express four calls. The price is paid in the image: the control-plane image
carries the Google Cloud CLI and git and comes to 819 MB.

**Docker** builds with the local daemon and runs a detached container with a
published port. There is no registry, so the reported image reference is the tag
it was asked for. It exists to make the development loop fast and free.

**Cloud Run** submits the build context to Cloud Build, which pushes to Artifact
Registry, and deploys the result as a Cloud Run service. Most of the driver is
translation between what the engine means and what Google Cloud accepts, and each
piece is there for a specific rejection:

| Engine | Google Cloud | Why |
| --- | --- | --- |
| `PORT` in the environment | dropped, `--port` instead | Cloud Run sets `PORT` itself and fails a deploy that also passes it |
| `launchpad.deployment` label | `launchpad_deployment` | a dot is not legal in a Google Cloud label key |
| `launchpad-<subdomain>` name | truncated to 49 characters with a digest | revisions are named `<service>-<suffix>` and must fit a DNS label |
| environment values | `^\|^`-delimited | gcloud splits on commas unless a delimiter is declared |
| local tag | Artifact Registry reference | Cloud Run pulls from a registry, so the pushed reference is what gets released and recorded |

Building in the cloud rather than locally is what lets the control plane run as a
Cloud Run service itself, with no Docker daemon of its own.

The gcloud command is echoed into the build log. A local Docker build can be
reproduced by reasoning about it; a Cloud Build that failed for one project in
one region cannot, so the log carries the line an operator can paste.
Environment values are redacted from that echo, because build logs are readable
by anyone who can read the deployment.

## Isolation and routing

Every environment answers on its own hostname under the platform's domain, so a
preview and a production deployment of the same service can never be reached at
one address: the isolation boundary is visible in the URL, not only in the
database. A partial unique index enforces at most one production environment per
project; previews are unbounded.

A deployment records two addresses. The public one is derived from the
environment's subdomain and survives redeployment; the internal one is wherever
the driver happened to put the workload and changes with every release.
Separating them is what keeps an environment's address stable while the thing
behind it is replaced.

The proxy caches a resolved upstream for five seconds — long enough to matter
under load, short enough that traffic follows a new deployment within a deploy's
worth of time. An environment that exists but has nothing serving yet answers
`503` with `Retry-After`; a hostname that was never minted answers `404`. Those
are different situations and deserve different answers.

Ownership is enforced in SQL. Every query is scoped by account in its `WHERE`
clause, and creating a deployment joins service to environment through their
shared project in one statement. There is no authorization check for a handler to
forget, because a row belonging to someone else simply does not come back.
Resources owned by another account return `404` rather than `403`, so a caller
cannot confirm that an ID exists.

## Measurement

Measurement happens at the proxy, because every request to a deployed workload
already passes through it. Nothing has to be installed in the deployed
application, and a workload that has stopped answering is measured by the same
code that measured it while it was healthy — an agent inside the container would
go quiet at exactly the moment its numbers mattered.

Each minute of each deployment's traffic becomes one row holding counts against
fixed latency buckets. Percentiles are interpolated from the buckets when a
window is read, because percentiles do not add up: the mean of two minutes' p95
is not the p95 of the two minutes together, but the sum of their histograms is
exactly the distribution of both.

The analysis ranks remediations by the traffic each finding affects, in requests
per hour, derived from the window's own distribution. It is deliberately not an
estimate of how well a fix will work: the platform can measure how much traffic a
problem affects and cannot see the future. Below twenty requests it reports
`insufficient_data` rather than health, because an environment nobody has called
has not been shown to work.

## How long a deployment actually takes

Measured by `TestPipelineEndToEnd`, which runs the whole pipeline — real git,
real detection, real Docker — and asserts the container answers HTTP before
reporting where the time went:

```bash
make test-deploy
```

Apple M2 Pro, 10 cores, 16 GB, macOS 15.6.1, Docker 29.5.2 with the classic
builder. A Go service with no Dockerfile of its own, so the generated one is
built. Times are what the test printed, not estimates.

| Phase | Cold | Warm |
| --- | --- | --- |
| fetch | 0.36s | 0.37–0.46s |
| build | 26.06s | 15.22–15.77s |
| release | 0.23s | 0.20–0.22s |
| everything else | 0.01s | 0.01s |
| **total** | **26.66s** | **15.91–16.38s** |

Cold is one run with `docker builder prune -af` and the base images removed, so
it includes pulling `golang:1.26-alpine` and `alpine:3.22`. Warm is three
consecutive runs afterwards.

Two things worth saying about these numbers.

**The platform's own orchestration costs about ten milliseconds.** Claiming the
job, detecting the strategy, three state transitions, the log writes and marking
the deployment live together account for 0.06% of a deployment. There is nothing
to optimise here, and any effort spent on the control plane's speed would be
effort spent on the wrong thing.

**The warm build should be faster than it is, and `.git` is not the reason.**
Fifteen of those sixteen seconds are `go build` inside the image, re-running even
though the source is byte-identical between runs: the `COPY . .` layer misses the
cache on every deployment, and every layer after it is rebuilt.

The obvious suspect was the checkout's `.git`, which goes into the build context
and differs between fetches, and an earlier version of this document named it as
the cause. It is not. Excluding `.git` takes a two-file repository's context from
56.4 kB to 3.7 kB and `COPY` still misses. It still misses when the two checkouts
are made identical down to the last mtime — verified by copying each context into
an image and diffing every path, size, mtime, mode and owner that arrived, which
reported no difference at all. Two plain directories with identical contents that
git never touched *do* share the cache, so the builder is capable of it here.
What is still different in the failing case is the excluded `.git` sitting on
disk beside the source and the `.dockerignore` that excludes it. That is where
the measurement stops: the cause is not established, and the fifteen seconds are
still on the table.

`.git` is excluded from generated builds anyway, for a better reason than speed.
The Python and Node strategies copy the whole tree into the image they deploy, so
a deployed container was shipping the repository's git metadata inside itself —
`.git/config` names the remote it was fetched from. A repository that supplies
its own Dockerfile keeps its metadata, because a build that stamps a version out
of git has to keep finding it.

## What is deliberately not built

**A queue.** Deployments are rows in PostgreSQL claimed with `FOR UPDATE SKIP
LOCKED`. That is enough for correct concurrent workers without a second piece of
infrastructure to run, and the claim is transactional with the state change,
which a separate broker would not be.

**Log streaming.** Build logs are followed by polling with a cursor. A client
asks for everything after the last sequence number it saw, so a follower that
loses its connection reconnects and misses nothing, where a broken stream would
have to be replayed from the start.

**A separate worker service.** The deploy workers run in the API process. This
costs one always-on instance on Cloud Run, because CPU is throttled between
requests and a polling worker needs CPU between requests. That is cheaper than a
second deployable to build, release and operate.

**A generated API client.** The dashboard's *types* are generated from the
OpenAPI document and its transport is hand-written. A generated client reports
failures as result objects, and rebuilding the field-level 422 handling the forms
depend on would cost more than the sixty lines it replaced.

**Wildcard DNS and TLS in Terraform.** Cloud Run domain mappings do not accept
wildcards, so `*.base-domain` needs a global external load balancer with a
wildcard certificate. That belongs with whatever already terminates TLS for an
estate rather than being guessed at here.

# Launchpad

A self-serve deployment platform. Point it at a Git repository and it provisions
the database, builds the service, wires a subdomain, and hands back a live URL.

Launchpad exists because the gap between "my code works locally" and "my code is
running somewhere other people can reach" is still filled with hand-written CI
YAML, copy-pasted Terraform, and a DNS record someone forgot to document. The
platform collapses that into one API call and one dashboard button.

## What it does

- **Repository to live URL.** Clone, detect how to build, produce a container
  image, release it, and return a reachable address.
- **Preview and production environments.** Every environment is an isolation
  boundary. Its own database, its own subdomain, its own deployments. Nothing
  crosses between a pull-request preview and production.
- **Provisioned dependencies.** Databases and subdomains are created as part of
  the deployment, not as a prerequisite you arrange beforehand.
- **Typed service contracts.** The API is specified once and the dashboard's
  client is generated from that spec, so a backend change that breaks the
  frontend fails at compile time instead of in the browser.
- **Latency and reliability analysis.** Deployed applications are measured
  continuously, and regressions are surfaced as a ranked list of what to fix
  first.

## Architecture

```
                  ┌──────────────────────┐
   Browser  ──────▶  Dashboard           │  React + TypeScript
                  │  (typed API client)  │  generated from OpenAPI
                  └──────────┬───────────┘
                             │ HTTPS
                  ┌──────────▼───────────┐
                  │  Control plane       │  Go
                  │  ─ REST API          │
                  │  ─ Deploy orchestr.  │
                  │  ─ Analysis engine   │
                  └─────┬───────────┬────┘
                        │           │
            ┌───────────▼──┐   ┌────▼─────────────────┐
            │  PostgreSQL  │   │  Deploy drivers      │
            │  platform    │   │  ─ Docker (local)    │
            │  state       │   │  ─ Cloud Run (prod)  │
            └──────────────┘   └────┬─────────────────┘
                                    │ Terraform modules
                               ┌────▼─────────────────┐
                               │  Deployed workloads  │
                               │  + provisioned DBs   │
                               │  + subdomains        │
                               └──────────────────────┘
```

The control plane is the only component that writes platform state. Deploy
drivers sit behind one interface, so the same orchestration logic runs a
container locally during development and a Cloud Run revision in production.

### Data model

`accounts` own `projects`. A project has `services` (deployable units — a
monorepo has several) and `environments` (`preview` or `production`). A
`deployment` is one service, built at one commit, released into one environment.
A partial unique index enforces at most one production environment per project;
previews are unbounded.

## Running locally

Requires Go 1.26+, Docker and Node 22+.

```bash
cp .env.example .env
make db-up      # start PostgreSQL
make run        # migrate on boot, then serve on :8080
make dashboard  # in another shell: serve the UI on :5173
```

```bash
curl localhost:8080/healthz         # liveness — never touches the database
curl localhost:8080/readyz          # readiness — fails if PostgreSQL is unreachable
curl localhost:8080/v1/version      # build revision
curl localhost:8080/v1/openapi.yaml # the API's own specification
```

Run `make help` for every target.

### Getting a token

Minting an API key through the API requires already holding one, so the first
credential comes from an operator command with direct database access rather
than a public signup endpoint:

```bash
make bootstrap EMAIL=you@example.com NAME="Your Name"
```

The token is printed once and only its SHA-256 hash is stored. The dashboard
asks for that token on first load; there is no login to run, and nothing to
configure.

## Dashboard

`dashboard/` is a React + TypeScript single-page app. It lists and creates
projects, adds services and environments, triggers a deployment, and follows the
build log line by line until the deployment reaches a state it will not leave.

The dev server proxies `/v1` to the control plane, so the browser talks to one
origin and the API carries no CORS headers. See `dashboard/README.md`.

## API

Every route under `/v1` except `/v1/version` and `/v1/openapi.yaml` requires
`Authorization: Bearer <token>`.

The contract is [`control-plane/openapi/openapi.yaml`](control-plane/openapi/openapi.yaml).
It is embedded in the binary and served at `/v1/openapi.yaml`, so the document a
caller fetches always belongs to the build that answered them, and the
dashboard's TypeScript types are generated from it with `make api-client`.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/projects` | Create a project |
| `GET` | `/v1/projects` | List your projects |
| `GET` | `/v1/projects/{id}` | Fetch one project |
| `DELETE` | `/v1/projects/{id}` | Delete a project and everything under it |
| `POST` | `/v1/projects/{id}/services` | Add a deployable unit |
| `GET` | `/v1/projects/{id}/services` | List services |
| `POST` | `/v1/projects/{id}/environments` | Create a preview or production environment |
| `GET` | `/v1/projects/{id}/environments` | List environments |
| `GET` | `/v1/projects/{id}/deployments` | Deployment history |
| `POST` | `/v1/deployments` | Trigger a deployment |
| `GET` | `/v1/deployments/{id}` | Deployment status |
| `GET` | `/v1/deployments/{id}/logs` | Build logs, `?after=N` to resume |
| `POST` | `/v1/api-keys` | Mint a key |
| `GET` | `/v1/api-keys` | List keys, never the secrets |
| `DELETE` | `/v1/api-keys/{id}` | Revoke a key |

```bash
curl -X POST localhost:8080/v1/projects \
  -H "Authorization: Bearer $LAUNCHPAD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"slug":"demo","name":"Demo App","repo_url":"https://github.com/example/demo"}'
```

Invalid input returns `422` listing every rejected field at once, so a caller
correcting a form never has to resubmit to discover the next mistake:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "one or more fields are invalid",
    "request_id": "79b72ed2f34d2cc3a785b5243b7ca506",
    "fields": [
      {"field": "slug", "message": "must be lowercase letters, digits and hyphens, and may not start or end with a hyphen"},
      {"field": "name", "message": "is required"}
    ]
  }
}
```

### Tests

```bash
make test              # Go unit tests, no dependencies
make test-integration  # adds tests that run migrations against a live database
make dashboard-test    # dashboard tests
make api-client-check  # fails if the generated types are behind the spec
make check             # everything, before committing
```

Integration tests skip themselves unless `LAUNCHPAD_TEST_DATABASE_URL` is set,
so a clean checkout tests green without Docker running. CI sets it, so the
schema is exercised on every push.

## Design decisions

**Migrations are embedded in the binary and run on boot.** No separate migration
step to forget, and no external CLI in the deploy image. A Postgres advisory
lock means several replicas starting at once cannot apply the same migration
twice.

**Liveness and readiness are separate.** `/healthz` deliberately touches no
dependencies. A database blip should not convince an orchestrator to restart an
otherwise healthy process; it should only stop routing traffic to it.

**Deploy targets are an interface, not a build flag.** The Docker driver makes
the development loop fast and free. The Cloud Run driver is the same contract
against a different backend.

**Ownership is enforced in SQL, not in handlers.** Every query is scoped by
account in its `WHERE` clause, and creating a deployment joins service to
environment through their shared project in a single statement. A handler that
forgets an authorization check is a common way to leak data; here there is no
check to forget, because a row belonging to someone else simply does not come
back. Resources owned by another account return `404` rather than `403`, so a
caller cannot confirm that an ID exists.

**Deployments are fetched shallow, never cloned.** A full clone of a large
repository spends minutes and bandwidth retrieving history the build will never
read. The fetcher initialises an empty repository and pulls the single
requested commit at depth 1.

**A repository's own Dockerfile always wins.** Detection tries `Dockerfile`
first and only then falls back to a language heuristic. If the author
described their build, guessing instead would be both rude and wrong.
Generated Dockerfiles are multi-stage and drop to a non-root user, because a
platform that builds other people's code should not hand that code root.

**Status transitions are enforced in the UPDATE statement.** The expected
current status is part of the `WHERE` clause, so checking and writing are one
atomic operation. Verifying first and updating second would leave a window in
which another worker could change the row in between. Workers claim jobs with
`FOR UPDATE SKIP LOCKED`, so several can run without ever building the same
deployment twice.

**Build logs are followed by polling with a cursor, not streamed.** The client
asks for everything after the last sequence number it has seen, so a follower
that loses its connection reconnects and misses nothing, where a broken stream
would have to be replayed from the start. Following deliberately continues for a
few polls after the status becomes terminal: the engine marks a deployment live
and only then flushes its final lines, so the line naming the URL arrives *after*
the status that says the deployment is finished.

**The specification is checked against the code, not written beside it.** A Go
test walks the router's route table and the document's paths and fails on any
endpoint one has and the other does not — including a route the document calls
public that the router authenticates. A second compares each schema's properties
against the JSON tags of the struct the handlers encode, and its `required` list
against the fields carrying no `omitempty`. CI regenerates the dashboard's types
and fails if the committed ones differ. A specification nobody verifies is a
comment that happens to be YAML.

**The specification generates the dashboard's types, not its transport.**
`src/api/types.ts` is aliases into a generated file, and the client's paths are
constrained to the ones the document declares, so a renamed route or field fails
`tsc` instead of the browser. The fetch layer stays hand-written: a generated
client reports failures as result objects rather than throwing, and rebuilding
the field-level 422 handling the forms depend on would cost more than the sixty
lines it replaced. Generating the part that has to be exact and writing the part
that has to be pleasant is cheaper than either alone.

**Environments are reached through a proxy, never directly.** Each environment
answers on its own hostname under the platform's domain, so a preview and a
production deployment of the same service can never be reached at one address:
the isolation boundary is visible in the URL, not only in the database.

A deployment records two addresses. The public one belongs to the environment
and survives redeployment; the internal one is wherever the driver happened to
put the workload and changes with every release. Separating them is what keeps
an environment's address stable while the thing behind it is replaced.

An environment that exists but has nothing serving yet answers `503` with
`Retry-After`, not `404`. A hostname nobody has deployed to and a hostname that
was never minted deserve different answers.

**API keys are stored only as hashes.** A leaked database dump yields no usable
credential. A plain SHA-256 is right here where it would be wrong for a
password: the token is 256 bits of uniform randomness, so there is no
dictionary to attack and nothing for a slow KDF to defend against.

## Status

Built in public over seven days. Each day is a working increment.

- [x] **Day 1** — Repository scaffold, Go control plane, PostgreSQL schema,
      embedded migrations, health endpoints, CI
- [x] **Day 2** — Domain model, REST API, API-key authentication, ownership
      isolation enforced in SQL
- [x] **Day 3** — Deploy engine: shallow fetch, build-strategy detection,
      Docker driver, state machine, streamed build logs
- [x] **Day 4** — React + TypeScript dashboard: project and deployment
      management, live build logs
- [x] **Day 5** — Preview environments, subdomain routing, typed contracts:
      an OpenAPI 3 specification the tests hold the code to, and the
      dashboard's types generated from it
- [ ] **Day 6** — Latency and reliability analysis
- [ ] **Day 7** — Terraform modules, Cloud Run driver, documentation

## License

MIT

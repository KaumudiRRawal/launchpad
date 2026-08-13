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

Requires Go 1.26+ and Docker.

```bash
cp .env.example .env
make db-up      # start PostgreSQL
make run        # migrate on boot, then serve on :8080
```

```bash
curl localhost:8080/healthz      # liveness — never touches the database
curl localhost:8080/readyz       # readiness — fails if PostgreSQL is unreachable
curl localhost:8080/v1/version   # build revision
```

Run `make help` for every target.

### Tests

```bash
make test              # unit tests, no dependencies
make test-integration  # adds tests that run migrations against a live database
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

## Status

Built in public over seven days. Each day is a working increment.

- [x] **Day 1** — Repository scaffold, Go control plane, PostgreSQL schema,
      embedded migrations, health endpoints, CI
- [ ] **Day 2** — Domain model, REST API, authentication
- [ ] **Day 3** — Deploy engine: clone, build, run, stream logs
- [ ] **Day 4** — React + TypeScript dashboard
- [ ] **Day 5** — Preview environments, subdomain routing, typed contracts
- [ ] **Day 6** — Latency and reliability analysis
- [ ] **Day 7** — Terraform modules, Cloud Run driver, documentation

## License

MIT

# Launchpad dashboard

React + TypeScript, built with Vite. Lists projects, creates them, adds services
and environments, triggers a deployment and follows its build log.

```bash
npm install
npm run dev          # http://localhost:5173
npm test
npm run build        # type-check, then bundle into dist/
npm run generate:api # regenerate src/api/schema.ts from the OpenAPI document
```

The dev server proxies `/v1` to the control plane on `localhost:8080`, so the
browser only ever talks to one origin and the API needs no CORS headers. Start
the control plane first with `make run` from the repository root.

## Layout

| Path | Holds |
| --- | --- |
| `src/api/` | Generated wire types and the only module that knows about HTTP |
| `src/hooks/` | Routing, resource loading, form submission, log following |
| `src/components/` | Pieces shared between views |
| `src/views/` | One file per screen |

## The generated client

`src/api/schema.ts` is generated from
`../control-plane/openapi/openapi.yaml` and is committed, so a build never
depends on codegen having been run. CI regenerates it with `npm run check:api`
and fails if the result differs from what is checked in — that is what stops the
two halves of the repository drifting apart.

`src/api/types.ts` is nothing but aliases into that file, and it is the only
module a component imports a wire type from. Keeping the generator's own naming
behind one boundary means swapping generators later touches one file.

`src/api/client.ts` constrains every request to a path the document declares, so
a route the control plane renames fails `tsc` here rather than 404ing in a
browser. The transport itself is written by hand: the errors this dashboard
renders are field-level 422 bodies, and a generated client that reports failures
as result objects would have to have that rebuilt on top of it.

Generation passes `--default-non-nullable false`. A field with a default —
`default_branch`, `source_path`, `port` — is optional to *send* and always
present in the *reply*, which the two schemas already say; without the flag the
generator reads the default as a promise and makes it required in both.

## Signing in

There is no login. The control plane authenticates with an API token, and the
first one is minted against the database by an operator:

```bash
make bootstrap EMAIL=you@example.com NAME="Your Name"
```

Paste that token into the dashboard. It is verified with a real request before
being accepted, so a truncated paste is reported immediately rather than looking
like an account with no projects.

# Launchpad dashboard

React + TypeScript, built with Vite. Lists projects, creates them, adds services
and environments, triggers a deployment and follows its build log.

```bash
npm install
npm run dev      # http://localhost:5173
npm test
npm run build    # type-check, then bundle into dist/
```

The dev server proxies `/v1` to the control plane on `localhost:8080`, so the
browser only ever talks to one origin and the API needs no CORS headers. Start
the control plane first with `make run` from the repository root.

## Layout

| Path | Holds |
| --- | --- |
| `src/api/` | Wire types and the only module that knows about HTTP |
| `src/hooks/` | Routing, resource loading, form submission, log following |
| `src/components/` | Pieces shared between views |
| `src/views/` | One file per screen |

Every wire type lives in `src/api/types.ts` and is mirrored by hand from the Go
structs in `internal/domain`. Components import their types from there and from
nowhere else, so those declarations can be replaced by generated ones without
touching anything above them.

## Signing in

There is no login. The control plane authenticates with an API token, and the
first one is minted against the database by an operator:

```bash
make bootstrap EMAIL=you@example.com NAME="Your Name"
```

Paste that token into the dashboard. It is verified with a real request before
being accepted, so a truncated paste is reported immediately rather than looking
like an account with no projects.

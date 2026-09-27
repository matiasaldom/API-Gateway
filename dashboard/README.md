# Gateway Dashboard

React + TypeScript + Vite + Recharts dashboard for the gateway's management API.

## Run it

The gateway must be running with `ADMIN_TOKEN` set (at least 32 characters).

```sh
cd dashboard
npm install
npm run dev                                   # http://localhost:5173, proxies to http://localhost:8080
GATEWAY_URL=http://localhost:9000 npm run dev # gateway somewhere else
```

Sign in with the gateway's `ADMIN_TOKEN`.

```sh
npm run typecheck   # tsc, strict mode
npm run build       # typecheck + production bundle in dist/
npm run preview     # serve dist/ with the same proxy
```

## How it talks to the gateway

The dashboard calls the existing `/admin/*` endpoints with relative paths. The Vite dev and preview servers proxy `/admin` to `GATEWAY_URL`, so the browser only ever talks to one origin. That's why the gateway needs no CORS support and no backend changes.

To host `dist/` elsewhere, serve it from the same origin as the gateway's `/admin` paths. For example, put a reverse proxy in front that sends `/admin/*` to the gateway and everything else to `dist/`.

- **Token storage:** the admin token is kept in `sessionStorage`, so it lasts for the tab and is gone when the tab closes. It's sent only as `Authorization: Bearer`.
- **Rejected token:** if any call returns `401` or `403`, the dashboard signs out.

## Pages

| Page | Data | Actions |
|---|---|---|
| Overview | `analytics/summary` for the chosen window, refreshed every 30 s. Live charts sample `summary?window=1m` every 10 s while the page is open. | — |
| Routes | `routes` merged with `analytics/routes`; requests and latency per route | Edit upstream and cache TTL |
| API Keys | `api-keys`, with a status filter and "Load more" paging | Create (the key is shown once, with a copy button), revoke |
| Plans | `plans` | Edit requests per minute |
| Accounts | `analytics/accounts`: top applications, plan utilization, per-application and per-key tables | — |

- **Live charts start empty:** the API has no time-series endpoint, so the live charts show only what was sampled since the page opened, at most the last 10 minutes.
- **Sortable tables:** click a column header to sort, and click again to reverse it. Sorting covers the rows already loaded.
- **Loading and errors:** every data view has a loading state and an error state. The error state shows the gateway's `request_id`, for looking it up in the logs, and a Retry button.
- **Validation:** server-side validation errors appear next to the field they belong to.

## Layout

```
src/
  api/types.ts        response types mirroring internal/admin and internal/metrics
  api/client.ts       typed AdminClient: one method per endpoint, ApiError with code/fields/request_id
  hooks.ts            useAsync (abortable, keeps stale data while reloading), useInterval, hash routing, dark mode
  components/         SortableTable, loading/error/empty states, dialog, chart frame
  pages/              Overview, Routes, ApiKeys, Plans, Accounts
  palette.ts          chart colors (validated reference palette, light and dark)
```

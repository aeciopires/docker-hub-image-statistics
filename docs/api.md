# HTTP API

All responses are JSON (`Content-Type: application/json`) unless noted. Paths
are relative, so the application also works behind a reverse proxy prefix.

<!-- TOC -->
- [Endpoints](#endpoints)
- [GET /api/v1/images](#get-apiv1images)
- [GET /api/v1/images/{namespace}/{name}](#get-apiv1imagesnamespacename)
- [GET /api/v1/images/{namespace}/{name}/history](#get-apiv1imagesnamespacenamehistory)
- [GET /api/v1/summary](#get-apiv1summary)
- [GET /api/v1/status](#get-apiv1status)
- [Errors](#errors)
<!-- TOC -->

## Endpoints

| Method and path | Description |
| --- | --- |
| `GET /` | Web UI |
| `GET /healthz`, `GET /livez` | Liveness: `{"status":"ok"}` |
| `GET /readyz` | Readiness: `200 {"status":"ready"}` or `503` when no cache tier works |
| `GET /metrics` | Prometheus/OpenMetrics |
| `GET /api/v1/version` | Version, commit, build date, Go version |
| `GET /api/v1/status` | Collector status (last/next update, errors, warnings) |
| `GET /api/v1/summary` | Totals of every image + status + version |
| `GET /api/v1/images` | Every image with total/1d/7d/30d pulls |
| `GET /api/v1/images/{namespace}/{name}` | One image |
| `GET /api/v1/images/{namespace}/{name}/history` | Hourly (or other step) series |

## GET /api/v1/images

Query parameters:

| Parameter | Values | Default |
| --- | --- | --- |
| `q` | substring matched against `namespace/name` and description | none |
| `sort` | `pulls`, `last`, `1d`, `7d`, `30d`, `stars`, `updated`, `name` | `pulls` |

```bash
curl -s 'http://localhost:8080/api/v1/images?sort=1d&q=grafana'
```

```json
{
  "images": [
    {
      "namespace": "grafana",
      "name": "grafana",
      "description": "The official Grafana docker container",
      "pulls_total": 5392808816,
      "pulls": {
        "1d":  {"pulls": 6630, "complete": true},
        "7d":  {"pulls": 51200, "complete": true},
        "30d": {"pulls": 51200, "complete": false}
      },
      "stars": 3594,
      "last_updated": "2026-11-16T09:44:08Z",
      "collected_at": "2026-11-17T11:00:00Z",
      "history_since": "2026-11-05T11:00:00Z",
      "last_collection": {"pulls": 7743, "previous_at": "2026-11-17T10:00:00Z"}
    }
  ],
  "count": 1
}
```

`complete: false` means the stored history is shorter than the window and the
value is a lower bound. `last_collection.pulls` is the increase between the two
newest snapshots (`previous_at` → `collected_at`); it is `0`, without
`previous_at`, for the first collection.

## GET /api/v1/images/{namespace}/{name}

Returns one object with the same shape as an item of `images`, or `404`.
Official images use the `library` namespace: `/api/v1/images/library/nginx`.

## GET /api/v1/images/{namespace}/{name}/history

| Parameter | Format | Default | Limits |
| --- | --- | --- | --- |
| `range` | duration (`24h`, `7d`, `30d`) | `24h` | up to `cache.retention` |
| `step` | duration (`1h`, `6h`, `1d`) | `1h` | 1 minute up to `range` |

```bash
curl -s 'http://localhost:8080/api/v1/images/library/nginx/history?range=24h&step=1h'
```

```json
{
  "image": {"namespace": "library", "name": "nginx", "pulls_total": 13603000000, "...": "..."},
  "step": "1h0m0s",
  "since": "2026-11-16T11:00:00Z",
  "points": [
    {"time": "2026-11-16T11:00:00Z", "pulls_total": 13602100000, "pulls": 0, "stars": 21422},
    {"time": "2026-11-16T12:00:00Z", "pulls_total": 13602140000, "pulls": 40000, "stars": 21422}
  ]
}
```

`pulls` is the increase compared with the previous point.

## GET /api/v1/summary

```json
{
  "summary": {
    "images": 102,
    "pulls_total": 180400000000,
    "pulls": {"1d": 676000, "7d": 676000, "30d": 676000},
    "pulls_last_collection": 826280,
    "stars": 296900,
    "collected_at": "2026-11-17T11:14:00Z"
  },
  "status": {"...": "see /api/v1/status"},
  "version": {"version": "1.0.0", "commit": "abc1234", "date": "2026-11-17T10:00:00Z", "go_version": "go1.27.4"}
}
```

## GET /api/v1/status

```json
{
  "collecting": false,
  "last_attempt": "2026-11-17T11:00:39Z",
  "last_success": "2026-11-17T11:00:39Z",
  "last_error": "",
  "warnings": ["docker hub only lists the first 100 repositories of \"library\" to anonymous clients; ..."],
  "last_duration": "2.175s",
  "images": 101,
  "interval": "1h0m0s",
  "next_run": "2026-11-17T12:00:42Z"
}
```

## Errors

Errors use the HTTP status code and a JSON body:

```json
{"error": "image not found"}
```

| Status | When |
| --- | --- |
| `400` | Invalid `sort`, `range` or `step` |
| `404` | Unknown image or API path |
| `405` | Method other than `GET` |
| `500` | Unexpected failure (details only in the logs) |
| `503` | `/readyz` when no cache tier works |

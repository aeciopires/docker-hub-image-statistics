#!/usr/bin/env python3
"""Generates the Grafana dashboards shipped with the Helm chart and compose stack.

Usage: python3 scripts/gen-dashboards.py   (or: make dashboards)
Output: charts/docker-hub-image-statistics/dashboards/*.json
"""
import json
import pathlib

OUT = pathlib.Path(__file__).resolve().parent.parent / "charts/docker-hub-image-statistics/dashboards"
PROM = {"type": "prometheus", "uid": "${datasource}"}
LOKI = {"type": "loki", "uid": "${loki}"}
TEMPO = {"type": "tempo", "uid": "${tempo}"}
SEL = 'namespace=~"$namespace",repository=~"$repository"'
SVC = 'service_name="docker-hub-image-statistics"'


class Grid:
    """Assigns ids and grid positions in reading order."""

    def __init__(self):
        self.panels, self.x, self.y, self.row_h, self.next_id = [], 0, 0, 0, 1

    def add(self, panel, w, h):
        if self.x + w > 24:
            self.x, self.y = 0, self.y + self.row_h
            self.row_h = 0
        panel["id"] = self.next_id
        panel["gridPos"] = {"x": self.x, "y": self.y, "w": w, "h": h}
        self.next_id += 1
        self.x += w
        self.row_h = max(self.row_h, h)
        self.panels.append(panel)

    def row(self, title):
        self.x, self.y = 0, self.y + self.row_h
        self.row_h = 0
        self.add({"type": "row", "title": title, "collapsed": False, "panels": []}, 24, 1)


def target(expr, legend="", ref="A", instant=False, fmt=None):
    t = {"datasource": PROM, "expr": expr, "legendFormat": legend, "refId": ref}
    if instant:
        t.update(instant=True, range=False)
    if fmt:
        t["format"] = fmt
    return t


def stat(title, expr, unit="short", desc="", color="blue", decimals=None):
    p = {
        "type": "stat", "title": title, "description": desc, "datasource": PROM,
        "targets": [target(expr, instant=True)],
        "fieldConfig": {"defaults": {"unit": unit, "color": {"mode": "fixed", "fixedColor": color},
                                     "thresholds": {"mode": "absolute", "steps": [{"color": color, "value": None}]}},
                        "overrides": []},
        "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                    "colorMode": "background", "graphMode": "none", "textMode": "value", "justifyMode": "center"},
    }
    if decimals is not None:
        p["fieldConfig"]["defaults"]["decimals"] = decimals
    return p


def timeseries(title, targets, unit="short", desc="", stack=False, draw="line"):
    return {
        "type": "timeseries", "title": title, "description": desc, "datasource": PROM, "targets": targets,
        "fieldConfig": {"defaults": {"unit": unit, "custom": {
            "drawStyle": draw, "lineWidth": 2, "fillOpacity": 15, "showPoints": "never",
            "stacking": {"mode": "normal" if stack else "none", "group": "A"}}}, "overrides": []},
        "options": {"legend": {"displayMode": "table", "placement": "bottom", "calcs": ["lastNotNull", "max", "sum"]},
                    "tooltip": {"mode": "multi", "sort": "desc"}},
    }


def bargauge(title, expr, legend, desc=""):
    return {
        "type": "bargauge", "title": title, "description": desc, "datasource": PROM,
        "targets": [target(expr, legend, instant=True)],
        "fieldConfig": {"defaults": {"unit": "short", "color": {"mode": "continuous-BlPu"}}, "overrides": []},
        "options": {"orientation": "horizontal", "displayMode": "gradient", "showUnfilled": True,
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}},
    }


def var_ds(name, ds_type, label):
    return {"name": name, "label": label, "type": "datasource", "query": ds_type, "hide": 0,
            "current": {}, "refresh": 1, "regex": "", "options": []}


def var_query(name, label, query):
    return {"name": name, "label": label, "type": "query", "datasource": PROM, "query": {"query": query, "refId": name},
            "definition": query, "refresh": 2, "includeAll": True, "multi": True, "allValue": ".*",
            "current": {"text": "All", "value": "$__all"}, "sort": 1, "hide": 0}


def dashboard(uid, title, desc, grid, variables, tags):
    return {
        "uid": uid, "title": title, "description": desc, "tags": tags, "editable": True,
        "graphTooltip": 1, "schemaVersion": 41, "version": 1, "refresh": "1m",
        "time": {"from": "now-7d", "to": "now"}, "timezone": "browser",
        "templating": {"list": variables}, "annotations": {"list": []},
        "links": [{"title": "Docker Hub Image Statistics", "type": "dashboards", "tags": ["docker-hub-image-statistics"],
                   "asDropdown": False, "includeVars": True, "keepTime": True},
                  {"title": "Jaeger UI", "type": "link", "url": "http://localhost:16686/search?service=docker-hub-image-statistics",
                   "targetBlank": True, "icon": "external link", "tooltip": "Traces in Jaeger (local compose stack)"}],
        "panels": grid.panels,
    }


def images_dashboard():
    g = Grid()
    g.row("Summary")
    g.add(stat("Total pulls", f"sum(dockerhub_image_pulls_total{{{SEL}}})", desc="Cumulative pulls reported by Docker Hub."), 3, 4)
    g.add(stat("Pulls (last collection)", f"sum(dockerhub_image_pulls_last_collection{{{SEL}}})",
               desc="Pulls between the two newest collections of each image.", color="semi-dark-blue"), 3, 4)
    g.add(stat("Pulls (1d)", f'sum(dockerhub_image_pulls{{{SEL},window="1d"}})', color="green"), 3, 4)
    g.add(stat("Pulls (7d)", f'sum(dockerhub_image_pulls{{{SEL},window="7d"}})', color="purple"), 3, 4)
    g.add(stat("Pulls (30d)", f'sum(dockerhub_image_pulls{{{SEL},window="30d"}})', color="orange"), 3, 4)
    g.add(stat("Images", f"count(dockerhub_image_pulls_total{{{SEL}}})", color="text"), 2, 4)
    g.add(stat("Stars", f"sum(dockerhub_image_stars{{{SEL}}})", color="yellow"), 2, 4)
    g.add(stat("Last update", "time() - max(dhis_last_collection_success_timestamp_seconds)", unit="s",
               desc="Time since the last successful collection.", color="text", decimals=0), 2, 4)
    g.add(stat("Next update in", "max(dhis_next_collection_timestamp_seconds) - time()", unit="s",
               desc="Time until the next scheduled collection (collector.interval, default 1h).", color="text", decimals=0), 3, 4)

    g.row("Hourly statistics")
    g.add(timeseries("Pulls per hour by image",
                     [target(f"sum by (namespace, repository) (increase(dockerhub_image_pulls_total{{{SEL}}}[1h]))",
                             "{{namespace}}/{{repository}}")],
                     desc="Increase of the Docker Hub pull counter in each hour.", stack=True, draw="bars"), 24, 9)
    g.add(timeseries("Pulls in the last collection by image",
                     [target(f"sum by (namespace, repository) (dockerhub_image_pulls_last_collection{{{SEL}}})",
                             "{{namespace}}/{{repository}}")],
                     desc="Pulls between the two newest collections (one point per collection interval).",
                     stack=True, draw="bars"), 8, 8)
    g.add(timeseries("Total pulls (cumulative)",
                     [target(f"sum by (namespace, repository) (dockerhub_image_pulls_total{{{SEL}}})",
                             "{{namespace}}/{{repository}}")]), 8, 8)
    g.add(timeseries("Pulls in window (1d / 7d / 30d)",
                     [target(f'sum by (window) (dockerhub_image_pulls{{{SEL}}})', "{{window}}")]), 8, 8)

    g.row("Top images")
    g.add(bargauge("Top 10 — last collection", f"topk(10, dockerhub_image_pulls_last_collection{{{SEL}}})",
                   "{{namespace}}/{{repository}}"), 6, 10)
    g.add(bargauge("Top 10 — last 24 hours", f'topk(10, dockerhub_image_pulls{{{SEL},window="1d"}})',
                   "{{namespace}}/{{repository}}"), 6, 10)
    g.add(bargauge("Top 10 — last 7 days", f'topk(10, dockerhub_image_pulls{{{SEL},window="7d"}})',
                   "{{namespace}}/{{repository}}"), 6, 10)
    g.add(bargauge("Top 10 — last 30 days", f'topk(10, dockerhub_image_pulls{{{SEL},window="30d"}})',
                   "{{namespace}}/{{repository}}"), 6, 10)

    g.row("Per image")
    table = {
        "type": "table", "title": "Images", "datasource": PROM,
        "description": "Window values marked incomplete in the app UI are lower bounds until enough history exists.",
        "targets": [
            # "sum by" gives every query the same label set so "merge" can join rows.
            target(f"sum by (namespace, repository) (dockerhub_image_pulls_total{{{SEL}}})", ref="A", instant=True, fmt="table"),
            target(f'sum by (namespace, repository) (dockerhub_image_pulls{{{SEL},window="1d"}})', ref="B", instant=True, fmt="table"),
            target(f'sum by (namespace, repository) (dockerhub_image_pulls{{{SEL},window="7d"}})', ref="C", instant=True, fmt="table"),
            target(f'sum by (namespace, repository) (dockerhub_image_pulls{{{SEL},window="30d"}})', ref="D", instant=True, fmt="table"),
            target(f"sum by (namespace, repository) (dockerhub_image_stars{{{SEL}}})", ref="E", instant=True, fmt="table"),
            target(f"max by (namespace, repository) (dockerhub_image_last_updated_timestamp_seconds{{{SEL}}}) * 1000", ref="F", instant=True, fmt="table"),
            target(f"sum by (namespace, repository) (dockerhub_image_pulls_last_collection{{{SEL}}})", ref="G", instant=True, fmt="table"),
        ],
        "transformations": [
            {"id": "merge", "options": {}},
            {"id": "organize", "options": {
                "excludeByName": {"Time": True, "__name__": True, "instance": True, "job": True, "window": True},
                "indexByName": {"namespace": 0, "repository": 1, "Value #A": 2, "Value #G": 3, "Value #B": 4,
                                "Value #C": 5, "Value #D": 6, "Value #E": 7, "Value #F": 8},
                "renameByName": {"namespace": "Namespace", "repository": "Repository", "Value #A": "Total pulls",
                                 "Value #G": "Last collection",
                                 "Value #B": "Pulls 1d", "Value #C": "Pulls 7d", "Value #D": "Pulls 30d",
                                 "Value #E": "Stars", "Value #F": "Last push"}}},
            {"id": "sortBy", "options": {"sort": [{"field": "Total pulls", "desc": True}]}},
        ],
        "fieldConfig": {"defaults": {"unit": "short"}, "overrides": [
            {"matcher": {"id": "byName", "options": "Last push"}, "properties": [{"id": "unit", "value": "dateTimeAsLocal"}]},
        ]},
        "options": {"showHeader": True, "cellHeight": "sm", "footer": {
            "show": True, "reducer": ["sum"],
            "fields": ["Total pulls", "Last collection", "Pulls 1d", "Pulls 7d", "Pulls 30d", "Stars"]}},
    }
    g.add(table, 24, 12)

    variables = [
        var_ds("datasource", "prometheus", "Prometheus"),
        var_query("namespace", "Namespace", "label_values(dockerhub_image_pulls_total, namespace)"),
        var_query("repository", "Repository", 'label_values(dockerhub_image_pulls_total{namespace=~"$namespace"}, repository)'),
    ]
    return dashboard("dhis-images", "Docker Hub Image Statistics",
                     "Pull statistics (total, 1d, 7d, 30d and hourly) of public Docker Hub images.",
                     g, variables, ["docker-hub-image-statistics", "docker"])


def service_dashboard():
    g = Grid()
    job = 'job=~"$job"'
    g.row("Health")
    up = stat("Up", f"min(up{{{job}}})", desc="1 when Prometheus can scrape the service.", color="green")
    up["fieldConfig"]["defaults"]["mappings"] = [{"type": "value", "options": {
        "0": {"text": "DOWN", "color": "red"}, "1": {"text": "UP", "color": "green"}}}]
    g.add(up, 4, 4)
    g.add(stat("Collections OK (24h)", f'sum(increase(dhis_collections_total{{{job},result="success"}}[24h])) or vector(0)', color="green", decimals=0), 4, 4)
    errs = stat("Collections with errors (24h)", f'sum(increase(dhis_collections_total{{{job},result="error"}}[24h])) or vector(0)', decimals=0)
    errs["fieldConfig"]["defaults"]["color"] = {"mode": "thresholds"}
    errs["fieldConfig"]["defaults"]["thresholds"] = {"mode": "absolute", "steps": [
        {"color": "green", "value": None}, {"color": "red", "value": 1}]}
    g.add(errs, 4, 4)
    g.add(stat("Images in last collection", "max(dhis_last_collection_images)", color="blue"), 4, 4)
    g.add(stat("Last update", "time() - max(dhis_last_collection_success_timestamp_seconds)", unit="s", color="text", decimals=0), 4, 4)
    g.add(stat("Next update in", "max(dhis_next_collection_timestamp_seconds) - time()", unit="s", color="text", decimals=0), 4, 4)

    g.row("Metrics")
    g.add(timeseries("Collection duration", [
        target(f"max(dhis_last_collection_duration_seconds{{{job}}})", "last collection"),
        target(f"histogram_quantile(0.95, sum by (le) (increase(dhis_collection_duration_seconds_bucket{{{job}}}[6h])))", "p95 (6h)", "B"),
    ], unit="s", desc="One collection runs per collector.interval (default 1h)."), 8, 8)
    g.add(timeseries("Cache operations by tier", [
        target("sum by (tier, op, result) (increase(dhis_cache_operations_total[$__rate_interval]))", "{{tier}} {{op}} {{result}}"),
    ]), 8, 8)
    g.add(timeseries("HTTP requests by route", [
        target(f"sum by (route, code) (rate(dhis_http_requests_total{{{job}}}[$__rate_interval]))", "{{route}} {{code}}"),
    ], unit="reqps"), 8, 8)
    g.add(timeseries("HTTP latency p95 by route", [
        target(f"histogram_quantile(0.95, sum by (le, route) (rate(dhis_http_request_duration_seconds_bucket{{{job}}}[$__rate_interval])))", "{{route}}"),
    ], unit="s"), 8, 8)
    g.add(timeseries("Memory", [
        target(f"process_resident_memory_bytes{{{job}}}", "RSS"),
        target(f"go_memstats_heap_alloc_bytes{{{job}}}", "heap", "B"),
    ], unit="bytes"), 8, 8)
    g.add(timeseries("CPU and goroutines", [
        target(f"rate(process_cpu_seconds_total{{{job}}}[$__rate_interval])", "CPU cores"),
        target(f"go_goroutines{{{job}}}", "goroutines", "B"),
    ]), 8, 8)

    g.row("Traces")
    g.add(timeseries("Span latency p95 (Tempo span metrics)", [
        target('histogram_quantile(0.95, sum by (le, span_name) (rate(traces_spanmetrics_latency_bucket{service="docker-hub-image-statistics"}[$__rate_interval])))',
               "{{span_name}}"),
    ], unit="s", desc="Requires the Tempo metrics-generator (enabled in the compose stack)."), 12, 9)
    traces = {
        "type": "table", "title": "Recent traces", "datasource": TEMPO,
        "targets": [{"datasource": TEMPO, "refId": "A", "queryType": "traceql", "limit": 20, "tableType": "traces",
                     "query": '{resource.service.name="docker-hub-image-statistics"}'}],
        "fieldConfig": {"defaults": {}, "overrides": []}, "options": {"showHeader": True},
    }
    g.add(traces, 12, 9)

    g.row("Logs")
    volume = {
        "type": "timeseries", "title": "Log volume by level", "datasource": LOKI,
        "targets": [{"datasource": LOKI, "refId": "A", "queryType": "range",
                     "expr": f"sum by (detected_level) (count_over_time({{{SVC}}} [$__auto]))", "legendFormat": "{{detected_level}}"}],
        "fieldConfig": {"defaults": {"custom": {"drawStyle": "bars", "fillOpacity": 60, "stacking": {"mode": "normal"}}},
                        "overrides": []},
        "options": {"legend": {"displayMode": "list", "placement": "bottom"}, "tooltip": {"mode": "multi"}},
    }
    g.add(volume, 24, 6)
    logs = {
        "type": "logs", "title": "Application logs", "datasource": LOKI,
        "targets": [{"datasource": LOKI, "refId": "A", "queryType": "range",
                     "expr": f'{{{SVC}}} |~ "$search"'}],
        "options": {"showTime": True, "wrapLogMessage": True, "enableLogDetails": True, "sortOrder": "Descending",
                    "dedupStrategy": "none", "prettifyLogMessage": False},
    }
    g.add(logs, 24, 12)

    variables = [
        var_ds("datasource", "prometheus", "Prometheus"),
        var_ds("loki", "loki", "Loki"),
        var_ds("tempo", "tempo", "Tempo"),
        {"name": "job", "label": "Job", "type": "query", "datasource": PROM,
         "query": {"query": "label_values(dhis_build_info, job)", "refId": "job"},
         "definition": "label_values(dhis_build_info, job)", "refresh": 2, "includeAll": False, "multi": False,
         "current": {}, "hide": 0},
        {"name": "search", "label": "Log filter (regex)", "type": "textbox", "query": "", "current": {"text": "", "value": ""}, "hide": 0},
    ]
    return dashboard("dhis-service", "Docker Hub Image Statistics — Service",
                     "Health, metrics, traces and logs of the docker-hub-image-statistics service.",
                     g, variables, ["docker-hub-image-statistics", "observability"])


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    for name, dash in {"docker-hub-image-statistics.json": images_dashboard(),
                       "docker-hub-image-statistics-service.json": service_dashboard()}.items():
        (OUT / name).write_text(json.dumps(dash, indent=2) + "\n")
        print(f"wrote {OUT / name} ({len(dash['panels'])} panels)")


if __name__ == "__main__":
    main()

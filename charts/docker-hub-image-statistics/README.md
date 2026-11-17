# docker-hub-image-statistics

![Version: 1.0.0](https://img.shields.io/badge/Version-1.0.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 1.0.0](https://img.shields.io/badge/AppVersion-1.0.0-informational?style=flat-square)

Collects and displays pull statistics (total, 1d, 7d, 30d and hourly) of public Docker Hub images, with Prometheus metrics, OpenTelemetry traces and JSON logs.

<!-- TOC -->
- [Requirements](#requirements)
- [Installing](#installing)
- [Exposing the service](#exposing-the-service)
  - [Gateway API (HTTPRoute)](#gateway-api-httproute)
  - [Other resources with extraObjects](#other-resources-with-extraobjects)
- [Docker Hub credentials](#docker-hub-credentials)
- [Observability](#observability)
- [Security](#security)
- [Testing](#testing)
- [Values](#values)
<!-- TOC -->

## Requirements

- **Helm 4** (`>= 4.0.0`). The chart refuses to render with Helm 3.
- Kubernetes >=1.30.0-0.

## Installing

```bash
helm upgrade --install dhis ./charts/docker-hub-image-statistics \
  --namespace dhis --create-namespace \
  --set 'config.dockerhub.namespaces={myorg}'
```

Open the UI with `kubectl -n dhis port-forward svc/dhis-docker-hub-image-statistics 8080:8080`.

## Exposing the service

Use `ingress.*` for an Ingress controller, or the Gateway API.

### Gateway API (HTTPRoute)

```yaml
httpRoute:
  enabled: true
  parentRefs:
    - name: public-gateway
      namespace: gateway-system
      sectionName: https
  hostnames: [stats.example.com]
```

### Other resources with extraObjects

`extraObjects` renders any manifest (map or string) through `tpl`, so it can
reference release values. Use it for `GRPCRoute`, `ReferenceGrant`,
`BackendTLSPolicy`, `ExternalSecret`, `PodMonitor`, etc.

```yaml
extraObjects:
  - apiVersion: gateway.networking.k8s.io/v1
    kind: ReferenceGrant
    metadata:
      name: "{{ include \"dhis.fullname\" . }}-from-gateway"
    spec:
      from: [{group: gateway.networking.k8s.io, kind: HTTPRoute, namespace: gateway-system}]
      to: [{group: "", kind: Service}]
```

## Docker Hub credentials

Anonymous clients may call the Docker Hub API 180 times per minute (per IP) and
only see the first 100 repositories of a namespace. Create a read-only personal
access token and reference it:

```bash
kubectl -n dhis create secret generic dhis-dockerhub \
  --from-literal=username=<user> --from-literal=token=<personal-access-token>
helm upgrade --install dhis ./charts/docker-hub-image-statistics -n dhis \
  --set dockerhub.existingSecret=dhis-dockerhub
```

## Observability

- `serviceMonitor.enabled=true` creates a Prometheus Operator `ServiceMonitor`.
- `grafanaDashboards.enabled=true` ships the dashboards as ConfigMaps for the Grafana sidecar.
- `config.telemetry.*` exports traces and logs over OTLP/HTTP (remember `networkPolicy.extraEgress`).

## Security

The defaults follow the Kubernetes *restricted* Pod Security Standard: non-root
UID 65532, read-only root filesystem, all capabilities dropped,
`seccompProfile: RuntimeDefault`, no service account token, a NetworkPolicy
allowing only HTTP ingress plus DNS/HTTPS egress, and a distroless image. Prefer
`image.digest` in production.

## Testing

```bash
helm test dhis -n dhis   # runs the image's own "healthcheck" command against /readyz
```

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Aecio Pires |  | <https://github.com/aeciopires> |

## Source Code

* <https://github.com/aeciopires/docker-hub-image-statistics>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity. |
| commonLabels | object | `{}` | Labels added to every resource. |
| config | object | see values.yaml | Application configuration rendered into config.yaml (see docs/configuration.md). Storage paths must live under /data, the only writable volume. |
| config.cache.retention | string | `"2160h"` | History retention (at least 744h to compute 30d pulls). |
| config.collector.interval | string | `"1h"` | Polling interval (minimum 5m anonymous, 1m authenticated). |
| config.dockerhub.namespaces | list | `["library"]` | Users/organizations whose public images are discovered. |
| config.dockerhub.repositories | list | `[]` | Explicit images ("namespace/name", or "name" for official images). |
| config.dockerhub.requests_per_minute | int | `0` | API requests per minute (0 = automatic: 60 anonymous, 150 authenticated; anonymous max 180). |
| config.log.level | string | `"info"` | Log level: debug, info, warn or error. |
| config.telemetry.otlp_endpoint | string | `""` | OTLP/HTTP endpoint, e.g. http://otel-collector.observability:4318. |
| dockerhub.existingSecret | string | `""` | Existing Secret with the keys "username" and "token" (takes precedence over username/token). |
| dockerhub.token | string | `""` | Docker Hub personal access token (read-only scope is enough). Prefer existingSecret. |
| dockerhub.username | string | `""` | Docker Hub username (optional; raises API rate limits and lifts the 100-repositories anonymous listing limit). |
| extraEnv | list | `[]` | Extra environment variables (DHIS_* override config). Values are rendered with tpl. |
| extraEnvFrom | list | `[]` | Extra envFrom sources. |
| extraObjects | list | `[]` | Extra Kubernetes objects rendered with tpl (strings or maps). Useful for Gateway API resources (GRPCRoute, ReferenceGrant, BackendTLSPolicy...), ExternalSecrets, PodMonitors, etc. |
| extraVolumeMounts | list | `[]` | Extra volume mounts. |
| extraVolumes | list | `[]` | Extra volumes. |
| fullnameOverride | string | `""` | Override the full resource name. |
| grafanaDashboards.annotations | object | `{"grafana_folder":"Docker Hub Image Statistics"}` | Annotations (for example grafana_folder). |
| grafanaDashboards.enabled | bool | `false` | Create ConfigMaps with the Grafana dashboards (picked up by the Grafana sidecar). |
| grafanaDashboards.labels | object | `{"grafana_dashboard":"1"}` | Label the Grafana sidecar watches. |
| httpRoute | object | `{"annotations":{},"enabled":false,"hostnames":["dhis.example.com"],"parentRefs":[{"name":"gateway","namespace":"gateway-system","sectionName":"https"}],"rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/"}}]}]}` | Gateway API HTTPRoute (gateway.networking.k8s.io/v1). For other Gateway API kinds use extraObjects. |
| httpRoute.annotations | object | `{}` | HTTPRoute annotations. |
| httpRoute.enabled | bool | `false` | Create an HTTPRoute. |
| httpRoute.hostnames | list | `["dhis.example.com"]` | Hostnames. |
| httpRoute.parentRefs | list | `[{"name":"gateway","namespace":"gateway-system","sectionName":"https"}]` | Gateways this route attaches to. |
| httpRoute.rules | list | `[{"matches":[{"path":{"type":"PathPrefix","value":"/"}}]}]` | Route rules. The backend is added automatically when backendRefs is empty. |
| image.digest | string | `""` | Image digest (sha256:...). When set it takes precedence over the tag (recommended for production). |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.registry | string | `"docker.io"` | Image registry. |
| image.repository | string | `"aeciopires/docker-hub-image-statistics"` | Image repository. |
| image.tag | string | `""` | Image tag (defaults to the chart appVersion). |
| imagePullSecrets | list | `[]` | Secrets used to pull the image from a private registry. |
| ingress.annotations | object | `{}` | Ingress annotations. |
| ingress.className | string | `""` | IngressClass name. |
| ingress.enabled | bool | `false` | Create an Ingress. |
| ingress.hosts | list | `[{"host":"dhis.example.com","paths":[{"path":"/","pathType":"Prefix"}]}]` | Ingress hosts. |
| ingress.tls | list | `[]` | Ingress TLS. |
| livenessProbe | object | `{"failureThreshold":3,"httpGet":{"path":"/healthz","port":"http"},"periodSeconds":20,"timeoutSeconds":3}` | Liveness probe. |
| nameOverride | string | `""` | Override the chart name. |
| networkPolicy.allowDNS | bool | `true` | Allow DNS egress (UDP/TCP 53). |
| networkPolicy.allowDockerHub | bool | `true` | Allow HTTPS egress to Docker Hub (TCP 443 to any address, since Docker Hub has no fixed IPs). |
| networkPolicy.enabled | bool | `true` | Create a NetworkPolicy (default deny except the rules below). |
| networkPolicy.extraEgress | list | `[]` | Extra egress rules (for example to the OpenTelemetry Collector). |
| networkPolicy.ingressFrom | list | `[]` | Ingress peers allowed to reach the HTTP port (empty = any). |
| nodeSelector | object | `{}` | Node selector. |
| persistence.accessModes | list | `["ReadWriteOnce"]` | Access modes. |
| persistence.annotations | object | `{}` | PVC annotations. |
| persistence.enabled | bool | `true` | Persist the SQLite/disk cache. When disabled an emptyDir is used (history is lost on restart). |
| persistence.existingClaim | string | `""` | Use an existing PersistentVolumeClaim. |
| persistence.size | string | `"1Gi"` | Volume size. |
| persistence.storageClass | string | `""` | StorageClass (empty uses the cluster default). |
| podAnnotations | object | `{}` | Pod annotations. |
| podLabels | object | `{}` | Pod labels. |
| podSecurityContext | object | `{"fsGroup":65532,"fsGroupChangePolicy":"OnRootMismatch","runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod security context (restricted Pod Security Standard). |
| priorityClassName | string | `""` | PriorityClass name. |
| readinessProbe | object | `{"failureThreshold":3,"httpGet":{"path":"/readyz","port":"http"},"periodSeconds":10,"timeoutSeconds":3}` | Readiness probe (checks the cache tiers). |
| replicaCount | int | `1` | Number of replicas. The SQLite cache is a single-writer store: keep 1 unless persistence is disabled. |
| resources | object | `{"limits":{"memory":"256Mi"},"requests":{"cpu":"25m","memory":"48Mi"}}` | Resource requests and limits. |
| revisionHistoryLimit | int | `3` | Revision history kept for rollbacks. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"privileged":false,"readOnlyRootFilesystem":true,"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Container security context (restricted Pod Security Standard). |
| service.annotations | object | `{}` | Service annotations. |
| service.port | int | `8080` | Service port. |
| service.type | string | `"ClusterIP"` | Service type. |
| serviceAccount.annotations | object | `{}` | ServiceAccount annotations. |
| serviceAccount.automountServiceAccountToken | bool | `false` | Mount the API token. The application never talks to the Kubernetes API. |
| serviceAccount.create | bool | `true` | Create a dedicated ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name (generated when empty). |
| serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator ServiceMonitor. |
| serviceMonitor.interval | string | `"60s"` | Scrape interval. |
| serviceMonitor.labels | object | `{}` | Extra labels (for example to match the Prometheus selector). |
| serviceMonitor.metricRelabelings | list | `[]` | Metric relabelings. |
| serviceMonitor.relabelings | list | `[]` | Relabelings. |
| serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| startupProbe | object | `{"failureThreshold":30,"httpGet":{"path":"/healthz","port":"http"},"periodSeconds":2}` | Startup probe. |
| terminationGracePeriodSeconds | int | `30` | Termination grace period (must exceed config.server.shutdown_timeout). |
| tests.enabled | bool | `true` | Enable "helm test" (uses the application image itself, no shell needed). |
| tmpSizeLimit | string | `"16Mi"` | Size limit of the /tmp emptyDir. |
| tolerations | list | `[]` | Tolerations. |
| topologySpreadConstraints | list | `[]` | Topology spread constraints. |

{{/*
Fail fast on Helm 3: this chart targets Helm 4 only.
*/}}
{{- define "dhis.requireHelm4" -}}
{{- $v := .Capabilities.HelmVersion.Version | default "v0.0.0" -}}
{{- if not (semverCompare ">=4.0.0-0" $v) -}}
{{- fail (printf "This chart requires Helm 4 (detected %s). Install Helm 4: https://helm.sh/docs/intro/install/" $v) -}}
{{- end -}}
{{- end -}}

{{- define "dhis.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dhis.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "dhis.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dhis.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dhis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "dhis.labels" -}}
helm.sh/chart: {{ include "dhis.chart" . }}
{{ include "dhis.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: docker-hub-image-statistics
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "dhis.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "dhis.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image reference: a digest wins over a tag; the tag defaults to the appVersion.
*/}}
{{- define "dhis.image" -}}
{{- $repo := printf "%s/%s" .Values.image.registry .Values.image.repository | trimPrefix "/" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $repo .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repo (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}
{{- end -}}

{{- define "dhis.secretName" -}}
{{- default (printf "%s-dockerhub" (include "dhis.fullname" .)) .Values.dockerhub.existingSecret -}}
{{- end -}}

{{- define "dhis.pvcName" -}}
{{- default (printf "%s-data" (include "dhis.fullname" .)) .Values.persistence.existingClaim -}}
{{- end -}}

{{/*
Renders a value that may be a string or a map through tpl (used by extraObjects).
*/}}
{{- define "dhis.render" -}}
{{- if typeIs "string" .value -}}
{{- tpl .value .context -}}
{{- else -}}
{{- tpl (.value | toYaml) .context -}}
{{- end -}}
{{- end -}}

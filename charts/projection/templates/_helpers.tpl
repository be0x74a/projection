{{/*
Expand the name of the chart.
*/}}
{{- define "projection.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
If release name contains chart name, it is used as-is; otherwise the chart name
is appended. Truncated to 63 chars to comply with the DNS-1035 Label Names spec.
*/}}
{{- define "projection.fullname" -}}
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

{{/*
Chart label: "name-version" suitable for helm.sh/chart.
*/}}
{{- define "projection.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels shared by all managed resources.
*/}}
{{- define "projection.labels" -}}
helm.sh/chart: {{ include "projection.chart" . }}
{{ include "projection.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: controller
app.kubernetes.io/part-of: projection
{{- end -}}

{{/*
Selector labels: stable subset used for matchLabels / service selectors.
*/}}
{{- define "projection.selectorLabels" -}}
app.kubernetes.io/name: {{ include "projection.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the ServiceAccount to use.
*/}}
{{- define "projection.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "projection.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Image reference, using Chart.AppVersion when .Values.image.tag is empty.
*/}}
{{- define "projection.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

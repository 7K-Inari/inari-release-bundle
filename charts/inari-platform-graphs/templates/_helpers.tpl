{{/*
Expand the name of the chart.
*/}}
{{- define "inari-platform-graphs.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels for every first-party resource.
*/}}
{{- define "inari-platform-graphs.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
app.kubernetes.io/name: {{ include "inari-platform-graphs.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: inari-platform
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

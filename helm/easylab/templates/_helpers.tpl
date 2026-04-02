{{/*
Expand the name of the chart.
*/}}
{{- define "easylab.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "easylab.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label value.
*/}}
{{- define "easylab.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "easylab.labels" -}}
helm.sh/chart: {{ include "easylab.chart" . }}
{{ include "easylab.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "easylab.selectorLabels" -}}
app.kubernetes.io/name: {{ include "easylab.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Namespace to use.
*/}}
{{- define "easylab.namespace" -}}
{{- .Values.namespace.name | default "easylab" }}
{{- end }}

{{/*
Image tag — defaults to chart appVersion with a leading v when missing, so it matches
Docker Hub tags from Git tags (e.g. appVersion 1.0.0 -> v1.0.0). User-set image.tag is used as-is.
*/}}
{{- define "easylab.imageTag" -}}
{{- if .Values.image.tag }}
{{- .Values.image.tag }}
{{- else }}
{{- $av := .Chart.AppVersion | toString }}
{{- if hasPrefix "v" $av }}
{{- $av }}
{{- else }}
{{- printf "v%s" $av }}
{{- end }}
{{- end }}
{{- end }}

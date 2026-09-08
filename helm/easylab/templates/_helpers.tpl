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
Selector labels for the ExternalDNS Deployment — distinct component label so
its selector never overlaps the main app Deployment's.
*/}}
{{- define "easylab.externaldnsSelectorLabels" -}}
{{ include "easylab.selectorLabels" . }}
app.kubernetes.io/component: externaldns
{{- end }}

{{/*
Name of the Traefik Middleware redirecting HTTP to HTTPS.
*/}}
{{- define "easylab.httpsRedirectMiddlewareName" -}}
{{- printf "%s-https-redirect" (include "easylab.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Whether to create the HTTP->HTTPS redirect Middleware and reference it from the
Ingress. Emits "true" (truthy) or "" (falsy), so callers can use it directly in
an if.

Gated on className being traefik, since Middleware is a Traefik CRD and every
other controller expresses this as its own annotation, and on tls.enabled,
since redirecting to HTTPS with no certificate configured only breaks the site
a different way.
*/}}
{{- define "easylab.httpsRedirectEnabled" -}}
{{- if and .Values.ingress.enabled .Values.ingress.tls.enabled .Values.ingress.httpsRedirect.enabled (eq .Values.ingress.className "traefik") -}}
true
{{- end -}}
{{- end }}

{{/*
The cross-provider reference Traefik expects in a router.middlewares annotation:
<namespace>-<name>@kubernetescrd.
*/}}
{{- define "easylab.httpsRedirectMiddlewareRef" -}}
{{- printf "%s-%s@kubernetescrd" (include "easylab.namespace" .) (include "easylab.httpsRedirectMiddlewareName" .) }}
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

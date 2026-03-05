{{/*
Expand the name of the chart.
*/}}
{{- define "selfhealing-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "selfhealing-operator.fullname" -}}
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
Common labels (Helm and Kubernetes recommended).
*/}}
{{- define "selfhealing-operator.labels" -}}
helm.sh/chart: {{ include "selfhealing-operator.name" . }}
app.kubernetes.io/name: {{ include "selfhealing-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/component: operator
app.kubernetes.io/part-of: ariadna-self-healing
{{- if .Chart.AppVersion }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- end }}

{{/*
Selector labels for deployment/pod.
*/}}
{{- define "selfhealing-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "selfhealing-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Namespace for the operator (create or use existing).
*/}}
{{- define "selfhealing-operator.namespace" -}}
{{- if .Values.namespace.create }}
{{- .Values.namespace.name | default "selfhealing-system" }}
{{- else }}
{{- .Release.Namespace }}
{{- end }}
{{- end }}

{{/*
Expand the name of the chart.
*/}}
{{- define "yatai-image-builder.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "yatai-image-builder.fullname" -}}
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

{{- define "yatai-image-builder.envname" -}}
yatai-image-builder-env
{{- end }}

{{- define "yatai-image-builder.shared-envname" -}}
yatai-image-builder-shared-env
{{- end }}

{{- define "yatai-image-builder.yatai-common-envname" -}}
yatai-common-env
{{- end }}

{{- define "yatai-image-builder.yatai-rolename-in-yatai-system-namespace" -}}
yatai-role-for-yatai-image-builder
{{- end }}

{{- define "yatai-image-builder.yatai-with-yatai-image-builder-rolename" -}}
yatai-with-yatai-image-builder
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "yatai-image-builder.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "yatai-image-builder.labels" -}}
helm.sh/chart: {{ include "yatai-image-builder.chart" . }}
{{ include "yatai-image-builder.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "yatai-image-builder.selectorLabels" -}}
app.kubernetes.io/name: {{ include "yatai-image-builder.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "yatai-image-builder.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "yatai-image-builder.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "yatai-image-builder.serviceAccountNameInYataiSystemNamespace" -}}
{{- printf "%s-in-yatai-system" (include "yatai-image-builder.serviceAccountName" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "yatai-image-builder.builderServiceAccountName" -}}
{{- if .Values.builder.serviceAccount.create }}
{{- default ((printf "%s-builder" (include "yatai-image-builder.fullname" .)) | trunc 63 | trimSuffix "-") .Values.builder.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.builder.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Generate k8s robot token
*/}}
{{- define "yatai-image-builder.yataiApiToken" -}}
    {{- $secretObj := (lookup "v1" "Secret" .Release.Namespace (include "yatai-image-builder.envname" .)) | default dict }}
    {{- $secretData := (get $secretObj "data") | default dict }}
    {{- (get $secretData "YATAI_API_TOKEN") | default (randAlphaNum 16 | nospace | b64enc) | b64dec }}
{{- end -}}

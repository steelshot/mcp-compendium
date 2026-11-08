{{- define "mcp-compendium.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "mcp-compendium.fullname" -}}
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

{{- define "mcp-compendium.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "mcp-compendium.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "mcp-compendium.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mcp-compendium.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "mcp-compendium.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "mcp-compendium.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* Image reference: <repository>/<flavour>, or fullRepository, with a digest or a tag. */}}
{{- define "mcp-compendium.image" -}}
{{- $repository := default (printf "%s/%s" .Values.image.repository .Values.image.flavour) .Values.image.fullRepository }}
{{- if .Values.image.digest }}
{{- printf "%s@%s" $repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" $repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
{{- end }}

{{- define "mcp-compendium.configMapName" -}}
{{- default (include "mcp-compendium.fullname" .) .Values.existingConfigMap }}
{{- end }}

{{- define "mcp-compendium.claimName" -}}
{{- default (include "mcp-compendium.fullname" .) .Values.persistence.existingClaim }}
{{- end }}

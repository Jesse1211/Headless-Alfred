{{/* Standard chart labels. */}}
{{- define "alfred.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{/* Full image reference, e.g. alfred/headless-alfred:local */}}
{{- define "alfred.image" -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repo .Values.image.tag -}}
{{- end -}}

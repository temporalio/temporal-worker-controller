{{/*
Common labels
Applied to all resources
*/}}
{{- define "temporal-worker-controller.labels" -}}
{{ include "temporal-worker-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
Used for matchLabels (Deployments, Services, affinities, etc.)
*/}}
{{- define "temporal-worker-controller.selectorLabels" -}}
app.kubernetes.io/name: temporal-worker-controller
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Namespace selector restricting admission webhooks to the watched namespaces.
Rendered only when rbac.restrictWatchNamespaces is set; keys off the
kubernetes.io/metadata.name label the API server sets on every namespace.
*/}}
{{- define "temporal-worker-controller.webhookNamespaceSelector" -}}
namespaceSelector:
  matchExpressions:
    - key: kubernetes.io/metadata.name
      operator: In
      values:
      {{- range .Values.rbac.restrictWatchNamespaces }}
        - {{ . | quote }}
      {{- end }}
{{- end }}

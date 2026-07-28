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
kube-rbac-proxy image reference. A digest takes precedence over a tag when set.
*/}}
{{- define "temporal-worker-controller.kubeRBACProxyImage" -}}
{{- if .Values.kubeRBACProxy.image.sha -}}
{{ printf "%s/%s@%s" .Values.kubeRBACProxy.image.registry .Values.kubeRBACProxy.image.repository .Values.kubeRBACProxy.image.sha }}
{{- else -}}
{{ printf "%s/%s:%s" .Values.kubeRBACProxy.image.registry .Values.kubeRBACProxy.image.repository .Values.kubeRBACProxy.image.tag }}
{{- end -}}
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

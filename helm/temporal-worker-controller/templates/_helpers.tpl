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
Return Role or ClusterRole depending on ownNamespace
*/}}
{{- define "temporal-worker-controller.rbac.roleKind" -}}
{{- if .Values.rbac.ownNamespace -}}
Role
{{- else -}}
ClusterRole
{{- end -}}
{{- end -}}

{{/*
Return RoleBinding or ClusterRoleBinding depending on ownNamespace
*/}}
{{- define "temporal-worker-controller.rbac.roleBindingKind" -}}
{{- if .Values.rbac.ownNamespace -}}
RoleBinding
{{- else -}}
ClusterRoleBinding
{{- end -}}
{{- end -}}

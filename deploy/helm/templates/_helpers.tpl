{{/*
Expand the name of the chart.
*/}}
{{- define "zstack-ovn-kubernetes.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "zstack-ovn-kubernetes.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "zstack-ovn-kubernetes.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "zstack-ovn-kubernetes.labels" -}}
helm.sh/chart: {{ include "zstack-ovn-kubernetes.chart" . }}
{{ include "zstack-ovn-kubernetes.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "zstack-ovn-kubernetes.selectorLabels" -}}
app.kubernetes.io/name: {{ include "zstack-ovn-kubernetes.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Controller labels
*/}}
{{- define "zstack-ovn-kubernetes.controller.labels" -}}
{{ include "zstack-ovn-kubernetes.labels" . }}
app.kubernetes.io/component: controller
{{- end }}

{{/*
Controller selector labels
*/}}
{{- define "zstack-ovn-kubernetes.controller.selectorLabels" -}}
{{ include "zstack-ovn-kubernetes.selectorLabels" . }}
app.kubernetes.io/component: controller
{{- end }}

{{/*
Node agent labels
*/}}
{{- define "zstack-ovn-kubernetes.node.labels" -}}
{{ include "zstack-ovn-kubernetes.labels" . }}
app.kubernetes.io/component: node
{{- end }}

{{/*
Node agent selector labels
*/}}
{{- define "zstack-ovn-kubernetes.node.selectorLabels" -}}
{{ include "zstack-ovn-kubernetes.selectorLabels" . }}
app.kubernetes.io/component: node
{{- end }}

{{/*
Create the name of the service account to use for controller
*/}}
{{- define "zstack-ovn-kubernetes.controller.serviceAccountName" -}}
{{- default (printf "%s-controller" (include "zstack-ovn-kubernetes.fullname" .)) .Values.controller.serviceAccount.name }}
{{- end }}

{{/*
Create the name of the service account to use for node agent
*/}}
{{- define "zstack-ovn-kubernetes.node.serviceAccountName" -}}
{{- default (printf "%s-node" (include "zstack-ovn-kubernetes.fullname" .)) .Values.node.serviceAccount.name }}
{{- end }}

{{/*
OVN NB DB address - returns the address based on mode
*/}}
{{- define "zstack-ovn-kubernetes.nbdbAddress" -}}
{{- if eq .Values.ovn.mode "external" }}
{{- .Values.ovn.nbdbAddress }}
{{- else }}
{{- printf "tcp:ovn-nb-db.%s.svc.cluster.local:6641" .Release.Namespace }}
{{- end }}
{{- end }}

{{/*
OVN SB DB address - returns the address based on mode
*/}}
{{- define "zstack-ovn-kubernetes.sbdbAddress" -}}
{{- if eq .Values.ovn.mode "external" }}
{{- .Values.ovn.sbdbAddress }}
{{- else }}
{{- printf "tcp:ovn-sb-db.%s.svc.cluster.local:6642" .Release.Namespace }}
{{- end }}
{{- end }}

{{/*
OVN standalone image
*/}}
{{- define "zstack-ovn-kubernetes.ovnImage" -}}
{{- printf "%s:%s" .Values.ovn.standalone.image.repository .Values.ovn.standalone.image.tag }}
{{- end }}

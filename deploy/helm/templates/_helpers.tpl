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
{{- if and .Values.controller .Values.controller.serviceAccount .Values.controller.serviceAccount.name }}
{{- .Values.controller.serviceAccount.name }}
{{- else }}
{{- printf "%s-controller" (include "zstack-ovn-kubernetes.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Create the name of the service account to use for node agent
*/}}
{{- define "zstack-ovn-kubernetes.node.serviceAccountName" -}}
{{- if and .Values.node .Values.node.serviceAccount .Values.node.serviceAccount.name }}
{{- .Values.node.serviceAccount.name }}
{{- else }}
{{- printf "%s-node" (include "zstack-ovn-kubernetes.fullname" .) }}
{{- end }}
{{- end }}

{{/*
OVN NB DB address - returns the address based on mode
For standalone mode, use localhost since controller runs on same node as DBs
*/}}
{{- define "zstack-ovn-kubernetes.nbdbAddress" -}}
{{- if eq .Values.ovn.mode "external" }}
{{- .Values.ovn.nbdbAddress }}
{{- else }}
{{- "tcp:127.0.0.1:6641" }}
{{- end }}
{{- end }}

{{/*
OVN SB DB address - returns the address based on mode
For standalone mode, use localhost since controller runs on same node as DBs
*/}}
{{- define "zstack-ovn-kubernetes.sbdbAddress" -}}
{{- if eq .Values.ovn.mode "external" }}
{{- .Values.ovn.sbdbAddress }}
{{- else }}
{{- "tcp:127.0.0.1:6642" }}
{{- end }}
{{- end }}

{{/*
OVN NB DB address for node agents - returns the address based on mode
For standalone mode, use the control-plane IP since node agents run on all nodes
*/}}
{{- define "zstack-ovn-kubernetes.nbdbAddressForNode" -}}
{{- if eq .Values.ovn.mode "external" }}
{{- .Values.ovn.nbdbAddress }}
{{- else if .Values.ovn.standalone.controlPlaneIP }}
{{- printf "tcp:%s:6641" .Values.ovn.standalone.controlPlaneIP }}
{{- else }}
{{- printf "tcp:ovn-nb-db.%s.svc.cluster.local:6641" .Release.Namespace }}
{{- end }}
{{- end }}

{{/*
OVN SB DB address for node agents - returns the address based on mode
For standalone mode, use the control-plane IP since node agents run on all nodes
*/}}
{{- define "zstack-ovn-kubernetes.sbdbAddressForNode" -}}
{{- if eq .Values.ovn.mode "external" }}
{{- .Values.ovn.sbdbAddress }}
{{- else if .Values.ovn.standalone.controlPlaneIP }}
{{- printf "tcp:%s:6642" .Values.ovn.standalone.controlPlaneIP }}
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

{{/*
Calculate default gateway from cluster CIDR
For 10.244.0.0/16, returns 10.244.0.1
*/}}
{{- define "zstack-ovn-kubernetes.defaultGateway" -}}
{{- $cidr := .Values.network.clusterCIDR -}}
{{- $parts := splitList "/" $cidr -}}
{{- $ip := index $parts 0 -}}
{{- $octets := splitList "." $ip -}}
{{- printf "%s.%s.%s.1" (index $octets 0) (index $octets 1) (index $octets 2) -}}
{{- end }}

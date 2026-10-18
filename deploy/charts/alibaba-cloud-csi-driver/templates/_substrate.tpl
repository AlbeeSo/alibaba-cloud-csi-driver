{{- define "substrate.drivers" -}}
{{- .base -}}{{- if .enabled -}}{{- if .base }},{{ end -}}substrate{{- end -}}
{{- end -}}

{{- define "substrate.commonVolumes" -}}
- name: substrate-trust-bundle
  projected:
    sources:
{{- if .node }}
      - clusterTrustBundle:
          signerName: servicedns.podcert.ate.dev/identity
          labelSelector:
            matchLabels: {podcert.ate.dev/canarying: live}
          path: trust-bundle.pem
{{- end }}
      - clusterTrustBundle:
          signerName: podidentity.podcert.ate.dev/identity
          labelSelector:
            matchLabels: {podcert.ate.dev/canarying: live}
          path: client-trust-bundle.pem
- name: substrate-identity
  projected:
    sources:
      - podCertificate:
          signerName: podidentity.podcert.ate.dev/identity
          keyType: ECDSAP256
          credentialBundlePath: credential-bundle.pem
{{- if .node }}
- name: substrate-api-token
  projected:
    sources:
      - serviceAccountToken:
          audience: {{ required "substrate.apiAudience is required" .Values.substrate.apiAudience | quote }}
          expirationSeconds: 3600
          path: token
{{- end }}
{{- end -}}

{{- define "substrate.commonMounts" -}}
- name: substrate-trust-bundle
  mountPath: /run/podidentity.podcert.ate.dev
  readOnly: true
{{- if .node }}
- name: substrate-api-token
  mountPath: /run/ateapi
  readOnly: true
{{- end }}
{{- end -}}



{{- define "substrate.apiArgs" -}}
- --substrate-api-endpoint={{ required "substrate.apiEndpoint is required" .Values.substrate.apiEndpoint }}
- --substrate-api-server-name={{ required "substrate.apiServerName is required" .Values.substrate.apiServerName }}
- --substrate-api-ca-file=/run/podidentity.podcert.ate.dev/trust-bundle.pem
- --substrate-api-token-file=/run/ateapi/token
{{- end -}}

{{- define "substrate.ossArgs" -}}
{{- if .Values.substrate.oss.enabled }}
- --substrate-oss-mount-proxy-sock={{ .Values.substrate.oss.mountProxySocket }}
{{- end }}
{{- end -}}

{{- define "substrate.ossVolumes" -}}
{{- if .Values.substrate.oss.enabled }}
- name: substrate-fuse-socket
  hostPath:
    path: {{ dir .Values.substrate.oss.mountProxySocket }}
    type: DirectoryOrCreate
- name: substrate-fuse-attach
  hostPath:
    path: {{ .Values.substrate.oss.fuseAttachDir }}
    type: DirectoryOrCreate
{{- end }}
{{- end -}}

{{- define "substrate.ossMounts" -}}
{{- if .Values.substrate.oss.enabled }}
- name: substrate-fuse-socket
  mountPath: {{ dir .Values.substrate.oss.mountProxySocket }}
- name: substrate-fuse-attach
  mountPath: {{ .Values.substrate.oss.fuseAttachDir }}
  mountPropagation: HostToContainer
{{- end }}
{{- end -}}

{{- define "substrate.ossEnv" -}}
{{- if .Values.substrate.oss.enabled }}
- name: AGENT_IDENTITY_ENDPOINT
  value: "https://credential-provider.ack-agent-identity.svc:8443"
{{- end }}
{{- end -}}

{{- define "substrate.validate" -}}
{{- if or (not (hasPrefix "/" .Values.substrate.actorRoot)) (eq (clean .Values.substrate.actorRoot) "/") -}}
{{- fail "substrate.actorRoot must be an absolute dedicated directory, not /" -}}
{{- end -}}
{{- end -}}

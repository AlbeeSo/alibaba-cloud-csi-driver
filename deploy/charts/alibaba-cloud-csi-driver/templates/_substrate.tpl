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

{{- define "substrate.validate" -}}
{{- $skipVersionCheck := "false" -}}
{{- if and .Values.enableSubstrate (eq $skipVersionCheck "false") (semverCompare "<1.37.0" .Capabilities.KubeVersion.Version) -}}
{{- fail (printf "enableSubstrate requires Kubernetes >= 1.37: the substrate node and controller pods project ClusterTrustBundle and PodCertificate volumes, which are not default-enabled before 1.37. Current cluster version: %s. If PodCertificateRequest and ClusterTrustBundleProjection feature gates are manually enabled on kube-apiserver, set $skipVersionCheck in templates/_substrate.tpl to true" .Capabilities.KubeVersion.Version) -}}
{{- end -}}
{{- if or (not (hasPrefix "/" .Values.substrate.root)) (eq (clean .Values.substrate.root) "/") -}}
{{- fail "substrate.root must be an absolute dedicated directory, not /" -}}
{{- end -}}
{{- end -}}

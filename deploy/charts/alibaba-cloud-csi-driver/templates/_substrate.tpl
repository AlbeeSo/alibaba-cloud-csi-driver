{{- define "substrate.apiVolume" -}}
- name: ateapi
  projected:
    sources:
      - clusterTrustBundle:
          signerName: servicedns.podcert.ate.dev/identity
          labelSelector:
            matchLabels:
              podcert.ate.dev/canarying: live
          path: trust-bundle.pem
      - serviceAccountToken:
          audience: {{ required "substrate.apiAudience is required" .Values.substrate.apiAudience | quote }}
          expirationSeconds: 3600
          path: token
{{- end -}}

{{- define "substrate.apiArgs" -}}
- --substrate-api-endpoint={{ required "substrate.apiEndpoint is required" .Values.substrate.apiEndpoint }}
- --substrate-api-ca-file=/run/ateapi/trust-bundle.pem
- --substrate-api-token-file=/run/ateapi/token
{{- end -}}

{{- define "substrate.validate" -}}
{{- if or (not (hasPrefix "/" .Values.substrate.actorRoot)) (eq (clean .Values.substrate.actorRoot) "/") -}}
{{- fail "substrate.actorRoot must be an absolute dedicated directory, not /" -}}
{{- end -}}
{{- end -}}

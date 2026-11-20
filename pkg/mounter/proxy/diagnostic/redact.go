// Package diagnostic removes credentials from client diagnostics before publication.
package diagnostic

import (
	"regexp"
	"sort"
	"strings"
)

var (
	accessID      = regexp.MustCompile(`\b(?:STS\.[A-Za-z0-9]+|LTAI[A-Za-z0-9]{8,}|(?:AKIA|ASIA)[A-Z0-9]{12,})\b`)
	jwt           = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	authorization = regexp.MustCompile(`(?i)(Authorization\s*[:=]\s*(?:Bearer|Basic|OSS)\s+)[^\s,"']+`)
	sensitiveArg  = regexp.MustCompile(`(?i)^--?(?:oss_access_key_id|oss_access_key_secret|oss_session_token|access[_-]?key[_-]?id|access[_-]?key[_-]?secret|secret[_-]?access[_-]?key|client[_-]?secret|security[_-]?token|oidc[_-]?token|session[_-]?token|access[_-]?token|refresh[_-]?token|token|password)$`)
	userInfo      = regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+@`)
	xmlValue      = regexp.MustCompile(`(?i)(<(?:AccessKeyId|AccessKeySecret|SecurityToken|OIDCToken|SessionToken|Password)>)[^<]*(</[A-Za-z_]+>)`)
	quotedValue   = regexp.MustCompile(`(?i)((?:"|')?(?:oss_access_key_id|oss_access_key_secret|oss_session_token|access_?key_?id|access_?key_?secret|secret_?access_?key|client_?secret|security_?token|oidc_?token|session_?token|access_?token|refresh_?token|token|password)(?:"|')?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*')`)
	bareValue     = regexp.MustCompile(`(?i)((?:oss_access_key_id|oss_access_key_secret|oss_session_token|access_?key_?id|access_?key_?secret|secret_?access_?key|client_?secret|security_?token|oidc_?token|session_?token|access_?token|refresh_?token|token|password)\s*[:=]\s*)[^\s,"'&<>]+`)
)

// Redact preserves operational context, but not known secret values or credential formats.
func Redact(message string, secrets map[string]string) string {
	values := make([]string, 0, len(secrets))
	for _, value := range secrets {
		if value != "" {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	message = xmlValue.ReplaceAllString(message, "${1}[redacted]${2}")
	message = quotedValue.ReplaceAllString(message, "${1}\"[redacted]\"")
	message = bareValue.ReplaceAllString(message, "${1}[redacted]")
	message = authorization.ReplaceAllString(message, "${1}[redacted]")
	message = userInfo.ReplaceAllString(message, "${1}[redacted]@")
	message = accessID.ReplaceAllString(message, "[redacted]")
	return jwt.ReplaceAllString(message, "[redacted]")
}

func Args(args []string, secrets map[string]string) []string {
	safe := make([]string, len(args))
	for i, arg := range args {
		if i > 0 && sensitiveArg.MatchString(args[i-1]) {
			safe[i] = "[redacted]"
		} else {
			safe[i] = Redact(arg, secrets)
		}
	}
	return safe
}

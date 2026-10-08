package plugin

import (
	"regexp"
	"strings"

	"qwen-cliproxyapi/internal/config"
	"qwen-cliproxyapi/internal/errclass"
)

var secretKeyPattern = regexp.MustCompile(`\bsk-[A-Za-z0-9_.-]+`)
var secretFieldPattern = regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|secret|cookie|set-cookie)["']*\s*[:=]\s*["']?)[^\r\n"']+`)

func redactSecrets(message string, cfg config.Config, selected string) string {
	// Exact matching also covers opaque keys without a recognizable prefix.
	secrets := []string{selected}
	for _, key := range cfg.APIKeys {
		secrets = append(secrets, key.Value)
	}
	for i, arg := range cfg.CommandArgs {
		lower := strings.ToLower(arg)
		for _, flag := range []string{"--cookie", "--token", "--api-key", "--password", "--secret"} {
			if lower == flag && i+1 < len(cfg.CommandArgs) {
				secrets = append(secrets, cfg.CommandArgs[i+1])
			}
			if strings.HasPrefix(lower, flag+"=") {
				secrets = append(secrets, arg[len(flag)+1:])
			}
		}
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = errclass.Redact(message)
	message = secretKeyPattern.ReplaceAllString(message, "[redacted]")
	return secretFieldPattern.ReplaceAllString(message, "${1}[redacted]")
}

func executionErrorEnvelope(e *errclass.Error, res *resolvedExecution) []byte {
	copyErr := *e
	copyErr.Message = redactSecrets(copyErr.Message, res.cfg, res.key)
	return classEnvelope(&copyErr)
}

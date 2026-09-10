package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

func upgradeSpecV5(raw []byte) ([]byte, error) {
	var document legacyV5TemplateSpec
	if err := DecodeJSON(bytes.TrimSpace(raw), &document); err != nil {
		return nil, errors.New("invalid TemplateSpec v5 document shape")
	}
	if document.SchemaVersion != 5 || (document.Endpoint.Scheme != "http" && document.Endpoint.Scheme != "https") {
		return nil, errors.New("invalid TemplateSpec v5 version or endpoint")
	}
	switch document.Kind {
	case "host":
		if document.Host == nil || document.Container != nil || document.Compose != nil || strings.TrimSpace(document.Host.StartScript) == "" {
			return nil, errors.New("invalid v5 Host deployment")
		}
	case "container":
		if document.Container == nil || document.Host != nil || document.Compose != nil {
			return nil, errors.New("invalid v5 container deployment")
		}
	case "compose":
		if document.Compose == nil || document.Host != nil || document.Container != nil {
			return nil, errors.New("invalid v5 Compose deployment")
		}
	default:
		return nil, errors.New("invalid v5 deployment")
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if hostRaw, exists := spec["host"]; exists && string(hostRaw) != "null" {
		var host map[string]json.RawMessage
		if err := json.Unmarshal(hostRaw, &host); err != nil {
			return nil, err
		}

		if targetRaw, exists := host["open_target"]; exists && string(targetRaw) != "null" {
			var target struct {
				Mode       string `json:"mode"`
				LinePrefix string `json:"line_prefix"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(targetRaw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&target); err != nil || target.Mode != "startup_output_url" || !validLegacyLinePrefix(target.LinePrefix) {
				return nil, errors.New("invalid v5 Host output target")
			}
			// Pass the literal prefix as a shell argument, never as executable awk
			// source or a regular expression. The runtime validates the resulting URL.
			prefix := "'" + strings.ReplaceAll(target.LinePrefix, "'", "'\"'\"'") + "'"
			after := "umask 077\n[ ! -s \"$REDEVEN_SERVICE_RUN_DIR/open-url\" ] || exit 0\nwhile :; do\n  url=$(head -c 16777216 \"$REDEVEN_SERVICE_OUTPUT_FILE\" | REDEVEN_LEGACY_PREFIX=" + prefix + " awk 'BEGIN { prefix=ENVIRON[\"REDEVEN_LEGACY_PREFIX\"] } index($0, prefix) == 1 { print substr($0, length(prefix)+1); exit }')\n  if [ -n \"$url\" ]; then\n    printf '%s\\n' \"$url\" > \"$REDEVEN_SERVICE_RUN_DIR/open-url.tmp\"\n    mv \"$REDEVEN_SERVICE_RUN_DIR/open-url.tmp\" \"$REDEVEN_SERVICE_RUN_DIR/open-url\"\n    exit 0\n  fi\n  sleep 0.1\ndone"
			host["after_start_script"], _ = json.Marshal(after)
			host["open_script"], _ = json.Marshal("cat \"$REDEVEN_SERVICE_RUN_DIR/open-url\"")
			host["output_mode"], _ = json.Marshal("private_file")
		}
		delete(host, "open_target")
		spec["host"], _ = json.Marshal(host)
	}
	spec["schema_version"] = json.RawMessage("6")
	return json.Marshal(spec)
}

func validLegacyLinePrefix(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

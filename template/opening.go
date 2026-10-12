package template

import (
	"bufio"
	"io"
	"net/url"
	"strings"
)

const MaxStartupOutputBytes = 16 * 1024 * 1024
const MaxStartupOutputLineBytes = 64 * 1024

func validateOpeningSource(spec Spec) error {
	hook := spec.Host != nil && spec.Host.OpenScript != ""
	if spec.StartupURL != nil {
		prefix := spec.StartupURL.LinePrefix
		if prefix == "" || len(prefix) > 256 || strings.ContainsAny(prefix, "\x00\r\n") || spec.Endpoint.Path != "" || hook {
			return fail("TEMPLATE_OPENING_INVALID", "Startup output requires one exact line prefix and cannot be combined with a fixed path or opening hook.")
		}
		if spec.Kind == DeploymentHost && spec.Host != nil && (spec.Host.OutputMode != "private_file" || spec.Host.AfterStartScript != "") {
			return fail("TEMPLATE_OPENING_INVALID", "Host startup output requires private output capture without an after-start hook.")
		}
	}
	if hook && spec.Endpoint.Path != "" {
		return fail("TEMPLATE_OPENING_INVALID", "An opening hook cannot be combined with a fixed path.")
	}
	return nil
}

// ParseStartupURL reads bounded output from one verified launch. It returns the
// first complete URL after the declared exact prefix; trailing LAN descriptions
// are not part of that URL. Errors never include output or credential values.
func ParseStartupURL(output io.Reader, source StartupURLSpec) (string, error) {
	if source.LinePrefix == "" {
		return "", fail("OPENING_SOURCE_INVALID", "The startup URL prefix is missing.")
	}
	limited := &io.LimitedReader{R: output, N: MaxStartupOutputBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), MaxStartupOutputLineBytes)
	var result string
	for scanner.Scan() {
		line := scanner.Text()
		if result != "" || !strings.HasPrefix(line, source.LinePrefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, source.LinePrefix))
		if len(fields) == 0 {
			continue
		}
		candidate := fields[0]
		u, err := url.Parse(candidate)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(candidate, "\x00\r\n") {
			return "", fail("OPENING_URL_INVALID", "The startup output contains an invalid opening URL.")
		}
		result = candidate
	}
	if scanner.Err() != nil || limited.N <= 0 {
		return "", fail("OPENING_OUTPUT_LIMIT", "The startup output is unavailable or exceeds its read limit.")
	}
	if result == "" {
		return "", fail("OPENING_URL_MISSING", "No opening URL remains in this launch's output. Restart explicitly to produce a new entrance.")
	}
	return result, nil
}

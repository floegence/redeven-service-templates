package template

import (
	"strings"
	"testing"
)

func TestStartupURLContract(t *testing.T) {
	raw := `{"schema_version":7,"kind":"container","endpoint":{"scheme":"http","container_port":3080},"startup_url":{"line_prefix":"dsh web: "},"container":{"image":"example/app:1","read_only_root":true}}`
	spec, version, err := NormalizeSpec([]byte(raw))
	if err != nil || version != 7 {
		t.Fatalf("new contract: version=%d err=%v", version, err)
	}
	for _, change := range []struct{ old, new string }{
		{`"schema_version":7`, `"schema_version":6`},
		{`"scheme":"http"`, `"scheme":"http","path":"/"`},
		{`"line_prefix":"dsh web: "`, `"line_prefix":""`},
	} {
		if _, _, err := NormalizeSpec([]byte(strings.Replace(raw, change.old, change.new, 1))); err == nil {
			t.Fatalf("accepted conflicting or historical declaration: %s", change.new)
		}
	}
	_ = spec
}

func TestParseStartupURL(t *testing.T) {
	source := StartupURLSpec{LinePrefix: "dsh web: "}
	const target = "http://127.0.0.1:3080/start?token=private#view"
	for _, text := range []string{"dsh web: " + target, "initializing\ndsh web: " + target + " (LAN: http://172.17.0.2:3080/?token=other)\n", "ignored dsh web: http://evil.example/\ndsh web: " + target + "\ndsh web: http://127.0.0.1:3080/?token=later\n"} {
		got, err := ParseStartupURL(strings.NewReader(text), source)
		if err != nil || got != target {
			t.Fatalf("opening URL was not extracted: %v", err)
		}
	}
	for _, text := range []string{"", "dsh web: not-a-url", "dsh web: http://user:secret@127.0.0.1:3080/", "dsh web: file:///etc/passwd", strings.Repeat("x", MaxStartupOutputLineBytes), strings.Repeat("short line\n", MaxStartupOutputBytes/10+2)} {
		if _, err := ParseStartupURL(strings.NewReader(text), source); err == nil {
			t.Fatal("accepted absent, invalid, or excessive output")
		}
	}
}

func TestDocumentOpeningCompatibilityPairs(t *testing.T) {
	for _, pair := range []struct {
		document, execution int
		valid               bool
	}{{3, 6, true}, {4, 7, true}, {3, 7, false}, {4, 6, false}, {2, 7, false}} {
		files := exampleFiles()
		files[0].Content = []byte(strings.Replace(strings.Replace(string(files[0].Content), `"schema_version":3`, `"schema_version":`+string(rune('0'+pair.document)), 1), `"schema_version":6`, `"schema_version":`+string(rune('0'+pair.execution)), 1))
		_, err := Read(files)
		if (err == nil) != pair.valid {
			t.Fatalf("document %d / execution %d: %v", pair.document, pair.execution, err)
		}
	}
}

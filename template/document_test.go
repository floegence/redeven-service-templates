package template

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func exampleFiles() []File {
	return []File{
		{Path: Filename, Mode: "100644", Content: []byte(`{"kind":"redeven.service-template","schema_version":3,"template_id":"example-web","service_family_id":"example-web","revision":1,"default_locale":"zh-CN","locales":["zh-CN"],"spec":{"schema_version":6,"kind":"host","endpoint":{"scheme":"http"},"host":{"start_script":"exec /bin/sh \"$REDEVEN_TEMPLATE_DIR/scripts/start.sh\""}}}`)},
		{Path: "locales/zh-CN.json", Mode: "100644", Content: []byte(`{"name":"示例服务","description":"一个中文默认语言的服务模板"}`)},
		{Path: "scripts/start.sh", Mode: "100755", Content: []byte("#!/bin/sh\nexec example-server\n")},
	}
}

func TestTransferDigestMatchesDesktopAndRejectsCaseParent(t *testing.T) {
	raw, err := os.ReadFile("testdata/source-transfer.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Files  []File `json:"files"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	actual, err := Digest(fixture.Files)
	if err != nil || actual != fixture.SHA256 {
		t.Fatalf("cross-language digest: %s %v", actual, err)
	}
	if _, err := Digest([]File{{Path: "A/child", Mode: "100644"}, {Path: "a", Mode: "100644"}}); err == nil {
		t.Fatal("case-insensitive file/directory conflict accepted")
	}
}

func TestHistoricalLifecyclePreservesLiteralPrefixAndExecutionFields(t *testing.T) {
	for version := 3; version <= 6; version++ {
		spec := map[string]any{"schema_version": version, "kind": "host", "endpoint": map[string]any{"scheme": "https", "health_path": "/ready", "startup_timeout_sec": 73}, "parameters": []any{map[string]any{"name": "PORT", "label": "Port", "type": "number", "default": "4321"}}, "host": map[string]any{"start_script": "exec app", "stop_script": "stop app", "install_script": "install app", "uninstall_script": "remove app", "environment": map[string]any{"EXAMPLE": "original"}}}
		host := spec["host"].(map[string]any)
		prefix := `URL ' $(touch should-not-exist) \\ `
		if version == 5 {
			host["open_target"] = map[string]any{"mode": "startup_output_url", "line_prefix": prefix}
		}
		raw, _ := json.Marshal(spec)
		normalized, original, err := NormalizeSpec(raw)
		if err != nil {
			t.Fatal(err)
		}
		if original != version || normalized.Parameters[0].Default != "4321" || normalized.Endpoint.HealthPath != "/ready" || normalized.Endpoint.StartupTimeout != 73 || normalized.Host.StartScript != "exec app" || normalized.Host.StopScript != "stop app" || normalized.Host.InstallScript != "install app" || normalized.Host.UninstallScript != "remove app" || normalized.Host.Environment["EXAMPLE"] != "original" {
			t.Fatal("historical lifecycle or defaults changed")
		}
		if version == 5 {
			root := t.TempDir()
			output := filepath.Join(root, "output")
			want := "https://127.0.0.1:4321/?token=private"
			if err := os.WriteFile(output, []byte(prefix+want+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			env := append(os.Environ(), "REDEVEN_SERVICE_RUN_DIR="+root, "REDEVEN_SERVICE_OUTPUT_FILE="+output)
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", normalized.Host.AfterStartScript)
			cmd.Dir = root
			cmd.Env = env
			if result, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("after-start: %v %s", err, result)
			}
			cmd = exec.CommandContext(ctx, "/bin/sh", "-c", normalized.Host.OpenScript)
			cmd.Env = env
			result, err := cmd.Output()
			if err != nil || string(result) != want+"\n" {
				t.Fatalf("opening changed: %v %s", err, result)
			}
			if _, err := os.Stat(filepath.Join(root, "should-not-exist")); !os.IsNotExist(err) {
				t.Fatal("historical prefix executed as shell code")
			}
		}
	}
}

func TestBrandedTemplatePreservesSourceAndDefaultLocale(t *testing.T) {
	files := exampleFiles()
	before, _ := json.Marshal(files)
	parsed, err := Read(files)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Document.Kind != Kind || parsed.Document.DefaultLocale != "zh-CN" || parsed.Spec.SchemaVersion != 6 {
		t.Fatalf("unexpected document: %+v", parsed)
	}
	if parsed.Localized("fr-FR").Name != "示例服务" {
		t.Fatal("template default locale was not used")
	}
	after, _ := json.Marshal(files)
	if !bytes.Equal(before, after) {
		t.Fatal("reading rewrote original source")
	}
	root := filepath.Join(t.TempDir(), "source")
	if err := WriteDirectory(root, files); err != nil {
		t.Fatal(err)
	}
	roundtrip, err := ReadDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Digest(files)
	b, _ := Digest(roundtrip)
	if a != b {
		t.Fatal("source bytes or executable modes changed on disk")
	}
}

func TestHistoricalReleasesRemainReadableWithoutChanges(t *testing.T) {
	manifestRaw, err := os.ReadFile("testdata/history/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	roots := map[string]bool{}
	for name, expected := range manifest {
		raw, err := os.ReadFile(filepath.Join("testdata/history", name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != expected {
			t.Fatalf("frozen historical input changed: %s", name)
		}
		if strings.HasSuffix(name, "/template.json") {
			roots[filepath.Dir(name)] = true
		}
	}
	for root := range roots {
		t.Run(root, func(t *testing.T) {
			files, err := ReadDirectory(filepath.Join("testdata/history", root))
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(files)
			parsed, err := Read(files)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Document.SchemaVersion != 3 || parsed.Spec.SchemaVersion != 6 || parsed.Document.DefaultLocale != "en-US" {
				t.Fatalf("historical template was not adapted: %+v", parsed.Document)
			}
			after, _ := json.Marshal(files)
			if !bytes.Equal(before, after) {
				t.Fatal("historical input changed during adaptation")
			}
			if parsed.Spec.Host != nil && parsed.SourceSpecVersion == 5 && (parsed.Spec.Host.AfterStartScript == "" || parsed.Spec.Host.OpenScript == "" || parsed.Spec.Host.OutputMode != "private_file") {
				t.Fatal("historical opening behavior was lost")
			}
			if parsed.Spec.Container != nil {
				for _, mount := range parsed.Spec.Container.Mounts {
					if mount.Type == "volume" && mount.ResourceID == "" {
						t.Fatal("legacy volume lost stable resource identity")
					}
				}
			}
		})
	}
}

func TestInvalidIdentityAndAmbiguousEntrypointsAreRejected(t *testing.T) {
	for _, tc := range []struct{ name, old, next string }{
		{"wrong brand", `"redeven.service-template"`, `"another.template"`},
		{"future document", `"schema_version":3`, `"schema_version":99`},
		{"future execution", `"schema_version":6`, `"schema_version":99`},
		{"missing default", `"default_locale":"zh-CN"`, `"default_locale":"fr-FR"`},
		{"unknown field", `"revision":1`, `"revision":1,"unexpected":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := exampleFiles()
			files[0].Content = bytes.Replace(files[0].Content, []byte(tc.old), []byte(tc.next), 1)
			if _, err := Read(files); err == nil {
				t.Fatal("invalid source was accepted")
			}
		})
	}
	files := exampleFiles()
	files = append(files, File{Path: "template.json", Mode: "100644", Content: files[0].Content})
	if _, err := Read(files); err == nil {
		t.Fatal("ambiguous entrypoints were accepted")
	}
}

func TestDirectoryDigestIncludesScriptsAndRejectsUnsafeFiles(t *testing.T) {
	files := exampleFiles()
	before, _ := Digest(files)
	files[2].Content = append(files[2].Content, []byte("# changed\n")...)
	after, _ := Digest(files)
	if before == after {
		t.Fatal("script-only update was invisible")
	}
	for _, f := range []File{{Path: "../escape", Mode: "100644"}, {Path: "link", Mode: "120000"}, {Path: "submodule", Mode: "160000"}, {Path: "large", Mode: "100644", Content: bytes.Repeat([]byte("x"), MaxFileBytes+1)}, {Path: "lfs", Mode: "100644", Content: []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\n")}} {
		if _, err := Digest(append(exampleFiles(), f)); err == nil {
			t.Fatalf("unsafe file accepted: %s", f.Path)
		}
	}
}

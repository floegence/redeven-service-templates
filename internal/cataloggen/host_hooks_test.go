package cataloggen

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedHostHooksCaptureAndReusePrivateOpeningInformation(t *testing.T) {
	raw, err := os.ReadFile("../../templates/deepseek-harness-host/redeven-service-template.json")
	if err != nil {
		t.Fatal(err)
	}
	var template struct {
		Spec struct {
			SchemaVersion int `json:"schema_version"`
			Host          struct {
				AfterStart string `json:"after_start_script"`
				Open       string `json:"open_script"`
				Output     string `json:"output_mode"`
			} `json:"host"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &template); err != nil {
		t.Fatal(err)
	}
	host := template.Spec.Host
	if template.Spec.SchemaVersion != 6 || host.Output != "private_file" || host.AfterStart == "" || host.Open == "" {
		t.Fatal("Host template must declare the independent-output lifecycle hooks")
	}
	root := t.TempDir()
	output := filepath.Join(root, "output")
	if err := os.WriteFile(output, []byte("initializing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-eu", "-c", host.AfterStart)
	cmd.Env = append(os.Environ(), "REDEVEN_SERVICE_RUN_DIR="+root, "REDEVEN_SERVICE_OUTPUT_FILE="+output)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	const target = "http://127.0.0.1:39191/?token=private-test-credential"
	if err := os.WriteFile(output, []byte("initializing\ndsh web: "+target+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(output, 0); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		open := exec.CommandContext(ctx, "/bin/sh", "-eu", "-c", host.Open)
		open.Env = cmd.Env
		result, err := open.Output()
		if err != nil || string(result) != target+"\n" {
			t.Fatalf("private opening result was not preserved: %v", err)
		}
	}
	info, err := os.Stat(filepath.Join(root, "open-url"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("opening information must remain private")
	}
}

package releasewatcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogPublication(t *testing.T) {
	script, err := filepath.Abs("../../scripts/publish_catalog.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "main-race", "tag-collision", "push-race"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "remote.git")
			repo := filepath.Join(root, "main")
			git := func(dir string, args ...string) string {
				t.Helper()
				command := exec.Command("git", args...)
				command.Dir = dir
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			git(root, "init", "--bare", "--initial-branch=main", remote)
			git(root, "clone", remote, repo)
			git(repo, "config", "user.name", "Test")
			git(repo, "config", "user.email", "test@example.invalid")
			hooks := filepath.Join(root, "hooks")
			_ = os.Mkdir(hooks, 0755)
			git(repo, "config", "core.hooksPath", hooks)
			_ = os.WriteFile(filepath.Join(repo, "catalog.go"), []byte("const Version = \"v0.4.1\"\n"), 0644)
			git(repo, "add", ".")
			git(repo, "commit", "-m", "test(catalog): baseline")
			git(repo, "push", "origin", "main")
			base := git(repo, "rev-parse", "HEAD")
			feature := filepath.Join(root, "candidate")
			git(repo, "worktree", "add", "-b", "codex/automation/test", feature, base)
			_ = os.WriteFile(filepath.Join(feature, "catalog.go"), []byte("const Version = \"v0.4.2\"\n"), 0644)
			git(feature, "add", ".")
			git(feature, "commit", "-m", "fix(catalog): update")
			tip := git(feature, "rev-parse", "HEAD")
			competitor := git(repo, "commit-tree", base+"^{tree}", "-p", base, "-m", "fix(test): competing main")
			git(repo, "push", "origin", competitor+":refs/heads/competitor")
			switch scenario {
			case "main-race":
				git(root, "--git-dir="+remote, "update-ref", "refs/heads/main", competitor)
			case "tag-collision":
				git(repo, "tag", "v0.4.2", base)
				git(repo, "push", "origin", "refs/tags/v0.4.2")
			case "push-race":
				hook := "#!/bin/sh\ngit --git-dir='" + remote + "' update-ref refs/heads/main " + competitor + "\n"
				_ = os.WriteFile(filepath.Join(hooks, "pre-push"), []byte(hook), 0755)
			}
			command := exec.Command("bash", script, base, "codex/automation/test")
			command.Dir = repo
			output, err := command.CombinedOutput()
			remoteMain := git(root, "--git-dir="+remote, "rev-parse", "refs/heads/main")
			tag := git(repo, "ls-remote", "--tags", "origin", "refs/tags/v0.4.2")
			if scenario == "success" {
				if err != nil || remoteMain != tip || tag == "" {
					t.Fatalf("publication failed: %v %s", err, output)
				}
				if git(repo, "rev-parse", "main") != git(repo, "rev-parse", "origin/main") {
					t.Fatal("main drift")
				}
			} else {
				if err == nil {
					t.Fatal("unsafe publication succeeded")
				}
				want := base
				if scenario == "main-race" || scenario == "push-race" {
					want = competitor
				}
				if remoteMain != want {
					t.Fatal("remote main was overwritten")
				}
				if scenario != "tag-collision" && tag != "" {
					t.Fatal("failed atomic push created tag")
				}
			}
		})
	}
}

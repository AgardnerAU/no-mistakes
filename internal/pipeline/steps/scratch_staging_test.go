package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestCommitAgentFixes_PlaywrightScratch(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, base, head, config.Commands{})
	// The first file predates the agent turn; the second is produced during it.
	if err := os.WriteFile(filepath.Join(dir, "preexisting.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".playwright-mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".playwright-mcp", "screen.png"), []byte("during"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new-code.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commitAgentFixes(sctx, types.StepTest, "apply test fix", "fallback"); err != nil {
		t.Fatal(err)
	}
	files := gitCmd(t, dir, "show", "--pretty=format:", "--name-only", "HEAD")
	if strings.Contains(files, ".playwright-mcp/") || !strings.Contains(files, "preexisting.txt") || !strings.Contains(files, "new-code.go") {
		t.Fatalf("commit files = %q", files)
	}
	if _, err := os.Stat(filepath.Join(dir, ".playwright-mcp", "screen.png")); err != nil {
		t.Fatal(err)
	}
}

func TestStagePipelineChanges_ScratchOnlyAndTrackedFiles(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, base, head, config.Commands{})
	scratch := filepath.Join(dir, "nested", ".playwright-mcp")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "screen.png"), []byte("scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged, err := stagePipelineChanges(sctx)
	if err != nil || staged {
		t.Fatalf("scratch-only staging = %v, %v", staged, err)
	}
	// A file deliberately tracked before the run remains eligible for updates.
	gitCmd(t, dir, "add", "-f", "nested/.playwright-mcp/screen.png")
	gitCmd(t, dir, "commit", "-m", "track fixture")
	if err := os.WriteFile(filepath.Join(scratch, "screen.png"), []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged, err = stagePipelineChanges(sctx)
	if err != nil || !staged {
		t.Fatalf("tracked update staging = %v, %v", staged, err)
	}
	if got := gitCmd(t, dir, "diff", "--cached", "--name-only"); got != "nested/.playwright-mcp/screen.png" {
		t.Fatalf("staged = %q", got)
	}
}

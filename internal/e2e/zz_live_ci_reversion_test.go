//go:build e2e

package e2e

// TEMPORARY live-validation driver for the test phase. Not part of the change.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const liveGuardBase = "#!/bin/sh\n# reviewed workflow pin\necho pinned-sha-aaaa1111 unique-base-line\n"
const liveGuardBranch = "#!/bin/sh\n# reviewed workflow pin\necho pinned-sha-bbbb2222 branch-decision\n"

func liveEvidenceDir(t *testing.T) string {
	dir := os.Getenv("LIVE_EVIDENCE_DIR")
	if dir == "" {
		t.Fatal("LIVE_EVIDENCE_DIR unset")
	}
	return dir
}

func liveWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(liveEvidenceDir(t), name), []byte(content), 0o644); err != nil {
		t.Fatalf("write evidence %s: %v", name, err)
	}
}

func liveScenario(t *testing.T, editPath, editContent string) string {
	path := filepath.Join(t.TempDir(), "ci-reversion-scenario.yaml")
	content := `actions:
  - match: "The following CI checks have failed on this PR"
    text: "restored guard.sh to make the pin check green"
    edits:
      - path: ` + editPath + `
        new: ` + strings.ReplaceAll(yamlDoubleQuoted(editContent), "\n", `\n`) + `
    structured:
      summary: "restore guard.sh so the build passes"
      code_change_needed: true
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks"
      risk_scope: source-or-external
      tested:
        - "fakeagent: simulated test run"
      testing_summary: "simulated tests passed"
      scenarios:
        - name: "fakeagent: simulated end-to-end scenario"
          result: pass
          live: true
          evidence: "fakeagent: simulated test run"
          reason: ""
      verdict: go
      artifacts: []
      title: "feat: pin guard"
      body: "## Summary\npin guard"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// installFailingCIForge wraps the stateful gh stub with a forge whose head
// commit has a failing build check and a failing declared decision check.
func installFailingCIForge(t *testing.T, h *Harness) string {
	realDir := filepath.Join(filepath.Dir(h.BinDir), "realgh")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(h.FakeAgent, filepath.Join(realDir, "gh")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	rollup := fmt.Sprintf(`{"data":{"repository":{"object":{"statusCheckRollup":{"contexts":{"nodes":[`+
		`{"__typename":"CheckRun","databaseId":21,"name":"build","status":"COMPLETED","conclusion":"FAILURE","startedAt":%[1]q,"completedAt":%[1]q,"detailsUrl":"https://github.com/example/reversion/actions/runs/11/job/21","checkSuite":{"app":{"slug":"github-actions"}}},`+
		`{"__typename":"CheckRun","databaseId":22,"name":"Workflow pin / verify (pull_request)","status":"COMPLETED","conclusion":"FAILURE","startedAt":%[1]q,"completedAt":%[1]q,"detailsUrl":"https://github.com/example/reversion/actions/runs/12/job/22","checkSuite":{"app":{"slug":"github-actions"}}}`+
		`],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}}`, now)
	prChecks := fmt.Sprintf(`[{"name":"build","state":"FAILURE","bucket":"fail","completedAt":%[1]q,"link":"https://github.com/example/reversion/actions/runs/11/job/21"},{"name":"Workflow pin / verify (pull_request)","state":"FAILURE","bucket":"fail","completedAt":%[1]q,"link":"https://github.com/example/reversion/actions/runs/12/job/22"}]`, now)
	forgeDir := filepath.Dir(h.AgentLog)
	rollupPath := filepath.Join(forgeDir, "rollup.json")
	prChecksPath := filepath.Join(forgeDir, "prchecks.json")
	logPath := filepath.Join(forgeDir, "gh-wrapper.log")
	if err := os.WriteFile(rollupPath, []byte(rollup), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prChecksPath, []byte(prChecks), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/bash
echo "$*" >> ` + shellQuote(logPath) + `
case "$*" in
  *"pr checks"*) cat ` + shellQuote(prChecksPath) + `; exit 0;;
  *statusCheckRollup*) cat ` + shellQuote(rollupPath) + `; exit 0;;
  *"run view"*"--json"*) echo '{"jobs":[]}'; exit 0;;
  *"run view 12"*) echo "PIN-LOG-MARKER: workflow pin awaits a maintainer approval of guard.sh"; exit 0;;
  *"run view"*) echo "BUILD-LOG-MARKER: build failed in feature.txt line 1"; exit 0;;
esac
exec ` + shellQuote(filepath.Join(realDir, "gh")) + ` "$@"
`
	ghPath := filepath.Join(h.BinDir, "gh")
	_ = os.Remove(ghPath)
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return logPath
}

func liveWriteGlobalConfig(t *testing.T, h *Harness) {
	cfg := fmt.Sprintf(`agent: claude
log_level: debug
agent_path_override:
  claude: %s
auto_fix:
  rebase: 0
  lint: 0
  test: 0
  review: 0
  document: 0
  ci: 2
`, filepath.Join(h.BinDir, "claude"))
	if err := os.WriteFile(filepath.Join(h.NMHome, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runAxiLong(t *testing.T, h *Harness, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.NMBin, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	h.syncDaemonOwnership()
	return string(out), err
}

func liveUpstreamFile(t *testing.T, h *Harness, branch, path string) string {
	out, err := h.runGit(context.Background(), h.UpstreamDir, "show", branch+":"+path)
	if err != nil {
		return "<unreadable: " + strings.TrimSpace(string(out)) + ">"
	}
	return string(out)
}

func ciFindings(t *testing.T, run *ipc.RunInfo) (types.Findings, string, types.StepStatus) {
	step, ok := findStep(run.Steps, types.StepCI)
	if !ok || step.FindingsJSON == nil {
		return types.Findings{}, "", step.Status
	}
	f, err := types.ParseFindingsJSON(*step.FindingsJSON)
	if err != nil {
		t.Fatalf("parse ci findings: %v", err)
	}
	return f, *step.FindingsJSON, step.Status
}

func liveHasID(f types.Findings, id string) bool {
	for _, item := range f.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func waitCIReversionPark(t *testing.T, h *Harness, runID string, after time.Time, timeout time.Duration) *ipc.RunInfo {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run := h.RunInfo(runID)
		if run != nil {
			f, _, status := ciFindings(t, run)
			if (status == types.StepStatusAwaitingApproval || status == types.StepStatusFixReview) && liveHasID(f, types.FindingIDCIDecisionReversion) {
				if inv := h.AgentInvocations(); len(inv) > 0 {
					last, _ := time.Parse(time.RFC3339Nano, inv[len(inv)-1].Time)
					if last.After(after) {
						return run
					}
				}
			}
			if run.Status.Terminal() {
				t.Fatalf("run ended %s: %s", run.Status, deref(run.Error))
			}
		}
		time.Sleep(time.Second)
	}
	if logs, err := h.Run("axi", "logs", "--step", "ci", "--full"); err == nil {
		liveWrite(t, "B-DIAG-ci-step-log.txt", logs)
	}
	run := h.RunInfo(runID)
	data, _ := json.MarshalIndent(run, "", "  ")
	liveWrite(t, "B-DIAG-run.json", string(data))
	branch := run.Branch
	liveWrite(t, "B-DIAG-upstream-guard.txt", liveUpstreamFile(t, h, branch, "guard.sh"))
	out, _ := h.runGit(context.Background(), h.UpstreamDir, "log", "--stat", "-5", branch)
	liveWrite(t, "B-DIAG-upstream-log.txt", string(out))
	t.Fatalf("CI step never parked at the reversion gate")
	return nil
}

func ciFixPrompts(h *Harness) []string {
	var out []string
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, "The following CI checks have failed") {
			out = append(out, inv.Prompt)
		}
	}
	return out
}


func liveSetup(t *testing.T, editPath, editContent string, ciRounds int) (*Harness, string) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: liveScenario(t, editPath, editContent)})
	liveWriteGlobalConfig(t, h)
	cfgPath := filepath.Join(h.NMHome, "config.yaml")
	data, _ := os.ReadFile(cfgPath)
	_ = os.WriteFile(cfgPath, []byte(strings.Replace(string(data), "ci: 2", fmt.Sprintf("ci: %d", ciRounds), 1)), 0o644)
	pushMainRepoConfig(t, h, "ignore_patterns:\n  - 'vendor/**'\nallow_repo_commands: true\nci:\n  decision_checks:\n    - \"workflow pin*\"\n")
	h.CommitChange("main", "guard.sh", liveGuardBase, "maintainer: add pinned guard")
	if out, err := h.runGit(context.Background(), h.WorkDir, "push", "origin", "main"); err != nil {
		t.Fatalf("push main: %v\n%s", err, out)
	}
	originURL := "https://github.com/example/reversion.git"
	configureGitURLRewrite(t, h, originURL, h.UpstreamDir)
	if out, err := h.runGit(t.Context(), h.WorkDir, "remote", "set-url", "origin", originURL); err != nil {
		t.Fatalf("set origin: %v\n%s", err, out)
	}
	t.Setenv("FAKEAGENT_GH_MODE", "stateful-pr")
	t.Setenv("FAKEAGENT_GH_STATE", filepath.Join(filepath.Dir(h.AgentLog), "gh-state.json"))
	t.Setenv("FAKEAGENT_GH_LOG", filepath.Join(filepath.Dir(h.AgentLog), "gh-stateful.log"))
	t.Setenv("FAKEAGENT_GH_PARENT", "example/reversion")
	ghLog := installFailingCIForge(t, h)
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return h, ghLog
}

func liveRunFor(h *Harness, branch, notID string) *ipc.RunInfo {
	for _, r := range h.Runs() {
		if r.Branch == branch && r.ID != notID {
			r := r
			return &r
		}
	}
	return nil
}

// Journey A: an ordinary CI repair of the branch's own code on a freshly
// pushed branch (the run's recorded base is git's zero SHA).
func TestLiveCIOrdinaryRepairOnNewBranch(t *testing.T) {
	h, _ := liveSetup(t, "feature.txt", "feature = fixed\n", 1)
	branch := "feature/ordinary"
	h.CommitChange(branch, "feature.txt", "feature = on\n", "add feature")
	start := time.Now()
	h.PushToGate(branch)
	deadline := time.Now().Add(4 * time.Minute)
	outcome := ""
	var run *ipc.RunInfo
	for time.Now().Before(deadline) && outcome == "" {
		time.Sleep(2 * time.Second)
		run = liveRunFor(h, branch, "")
		if run == nil {
			continue
		}
		if liveUpstreamFile(t, h, branch, "feature.txt") == "feature = fixed\n" {
			outcome = "committed"
			break
		}
		f, _, status := ciFindings(t, run)
		if (status == types.StepStatusAwaitingApproval || status == types.StepStatusFixReview) && liveHasID(f, types.FindingIDCIDecisionReversion) {
			outcome = "refused"
		}
		if run.Status.Terminal() {
			outcome = "terminal:" + string(run.Status)
		}
	}
	_ = start
	data, _ := json.MarshalIndent(h.RunInfo(run.ID), "", "  ")
	liveWrite(t, "A-run.json", string(data))
	if logs, err := h.Run("axi", "logs", "--step", "ci", "--full"); err == nil {
		liveWrite(t, "A-ci-step-log.txt", logs)
	}
	if st, err := h.Run("axi", "status"); err == nil {
		liveWrite(t, "A-axi-status.txt", st)
	}
	liveWrite(t, "A-outcome.txt", fmt.Sprintf("outcome=%s\nupstream feature.txt=%q\nrun base_sha=%s\n", outcome, liveUpstreamFile(t, h, branch, "feature.txt"), run.BaseSHA))
	if outcome != "committed" {
		t.Errorf("an ordinary CI repair of the branch's own code was not committed: outcome=%s", outcome)
	}
	if !run.Status.Terminal() {
		h.CancelRun(run.ID)
	}
}

// Journey B: a real byte-identical reversion on a run with a readable base.
func TestLiveCIReversionJourney(t *testing.T) {
	h, ghLog := liveSetup(t, "guard.sh", liveGuardBase, 2)
	branch := "feature/pin-guard"
	h.CommitChange(branch, "feature.txt", "feature = on\n", "add feature")
	h.PushToGate(branch)
	var first *ipc.RunInfo
	for i := 0; i < 60 && first == nil; i++ {
		time.Sleep(time.Second)
		first = liveRunFor(h, branch, "")
	}
	if first == nil {
		t.Fatal("first run never started")
	}
	h.CancelRun(first.ID)
	h.WaitForRun(branch, time.Minute)
	h.CommitChange(branch, "guard.sh", liveGuardBranch, "decision: move the pin to bbbb2222")
	start := time.Now()
	h.PushToGate(branch)
	var run *ipc.RunInfo
	for i := 0; i < 60 && run == nil; i++ {
		time.Sleep(time.Second)
		run = liveRunFor(h, branch, first.ID)
	}
	if run == nil {
		t.Fatal("second run never started")
	}
	liveWrite(t, "B-00-run-base.txt", fmt.Sprintf("run=%s base_sha=%s head_sha=%s\n", run.ID, run.BaseSHA, run.HeadSHA))

	// Scenario: an automatic driver (axi run --yes, reattaching) reaches the reversion gate.
	out, err := runAxiLong(t, h, h.WorkDir, 8*time.Minute, "axi", "run", "--yes", "--wait", "3m")
	liveWrite(t, "B-01-axi-run-yes.txt", out)
	if err != nil {
		t.Logf("axi run exit: %v", err)
	}
	run = waitCIReversionPark(t, h, run.ID, start, 2*time.Minute)
	_, raw, status := ciFindings(t, run)
	liveWrite(t, "B-02-ci-gate-findings.json", fmt.Sprintf("status=%s\n%s", status, raw))
	if !strings.Contains(raw, "guard.sh") || strings.Contains(raw, "could not be evaluated") {
		t.Errorf("reversion finding does not carry guard.sh evidence: %s", raw)
	}
	prompts := ciFixPrompts(h)
	liveWrite(t, "B-03-ci-fix-prompts.txt", strings.Join(prompts, "\n\n===== NEXT CI FIX PROMPT =====\n\n"))
	for i, p := range prompts {
		if strings.Contains(p, "PIN-LOG-MARKER") || !strings.Contains(p, "- failing checks: build\n") {
			t.Errorf("CI fix prompt %d exposes the decision check as a target or its logs", i)
		}
	}
	if !strings.Contains(out, "a CI repair that would undo the branch's own work requires an explicit response") {
		t.Errorf("axi --yes did not stand aside at the reversion gate")
	}
	if got := liveUpstreamFile(t, h, branch, "guard.sh"); got != liveGuardBranch {
		t.Errorf("upstream guard.sh after refusal = %q, want branch decision kept", got)
	}
	liveWrite(t, "B-04-upstream-guard-after-refusal.txt", liveUpstreamFile(t, h, branch, "guard.sh"))
	if st, serr := h.Run("axi", "status"); serr == nil {
		liveWrite(t, "B-05-axi-status-at-reversion-gate.txt", st)
	}
	fixPromptsBefore := len(prompts)

	// Scenario: TUI yolo stands aside.
	driver, _ := filepath.Abs(filepath.Join("testdata", "tui_yolo_driver.py"))
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	screen, derr := exec.CommandContext(ctx, "python3", driver, h.NMBin, h.WorkDir, "y", "6", "8").Output()
	cancel()
	liveWrite(t, "B-06-tui-yolo-screen.txt", lastScreenFrame(string(screen)))
	if derr != nil {
		t.Errorf("tui driver: %v", derr)
	}
	if !strings.Contains(string(screen), "end yolo") {
		t.Errorf("yolo never engaged")
	}
	time.Sleep(5 * time.Second)
	after := h.RunInfo(run.ID)
	af, araw, astatus := ciFindings(t, after)
	liveWrite(t, "B-07-ci-gate-after-tui-yolo.json", fmt.Sprintf("status=%s\n%s", astatus, araw))
	if !liveHasID(af, types.FindingIDCIDecisionReversion) || (astatus != types.StepStatusAwaitingApproval && astatus != types.StepStatusFixReview) {
		t.Errorf("TUI yolo resolved the reversion gate: status=%s", astatus)
	}
	if n := len(ciFixPrompts(h)); n != fixPromptsBefore {
		t.Errorf("TUI yolo started a fix round (%d -> %d)", fixPromptsBefore, n)
	}

	// Scenario: a fix response that does not select the reversion finding
	// authorises nothing; the identical reversion re-parks.
	respondAt := time.Now()
	h.RespondWithFindings(run.ID, types.StepCI, types.ActionFix, []string{"ci-2"})
	reparked := waitCIReversionPark(t, h, run.ID, respondAt, 3*time.Minute)
	pf, rpraw, rpstatus := ciFindings(t, reparked)
	liveWrite(t, "B-08-ci-gate-after-fix-without-reversion-selected.json", fmt.Sprintf("status=%s\n%s", rpstatus, rpraw))
	if got := liveUpstreamFile(t, h, branch, "guard.sh"); got != liveGuardBranch {
		t.Errorf("a fix response that did not select the reversion committed it: upstream guard.sh = %q", got)
	}
	liveWrite(t, "B-09-upstream-guard-after-unselected-fix.txt", liveUpstreamFile(t, h, branch, "guard.sh"))
	ids := []string{}

	// Scenario: a fix response at the gate that showed this refusal, in this
	// process, authorises exactly that reversion.
	ids = ids[:0]
	for _, item := range pf.Items {
		ids = append(ids, item.ID)
	}
	h.RespondWithFindings(run.ID, types.StepCI, types.ActionFix, ids)
	deadline := time.Now().Add(4 * time.Minute)
	var got string
	for time.Now().Before(deadline) {
		got = liveUpstreamFile(t, h, branch, "guard.sh")
		if got == liveGuardBase {
			break
		}
		time.Sleep(2 * time.Second)
	}
	liveWrite(t, "B-10-upstream-guard-after-authorised-fix.txt", got)
	if logs, lerr := h.Run("axi", "logs", "--step", "ci", "--full"); lerr == nil {
		liveWrite(t, "B-11-ci-step-log-final.txt", logs)
	}
	final := h.RunInfo(run.ID)
	data, _ := json.MarshalIndent(final, "", "  ")
	liveWrite(t, "B-12-run-final.json", string(data))
	if wl, _ := os.ReadFile(ghLog); len(wl) > 0 {
		liveWrite(t, "B-13-gh-wrapper.log", string(wl))
	}
	if got != liveGuardBase {
		t.Errorf("the authorised reversion was never published: upstream guard.sh = %q", got)
	}
	if !final.Status.Terminal() {
		h.CancelRun(final.ID)
	}
}

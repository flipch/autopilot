package autopilot

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	runOutputs map[string][]byte
	runErrors  map[string]error
	started    []invocation
	runs       []invocation
	lookups    map[string]error
}

type invocation struct {
	dir  string
	name string
	args []string
}

func (f *fakeRunner) Run(dir string, name string, args ...string) ([]byte, error) {
	call := invocation{dir: dir, name: name, args: append([]string{}, args...)}
	f.runs = append(f.runs, call)
	key := commandKey(name, args...)
	if err := f.runErrors[key]; err != nil {
		return nil, err
	}
	return f.runOutputs[key], nil
}

func (f *fakeRunner) Start(dir string, stdin io.Reader, stdout io.Writer, stderr io.Writer, name string, args ...string) error {
	f.started = append(f.started, invocation{dir: dir, name: name, args: append([]string{}, args...)})
	return nil
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if err := f.lookups[file]; err != nil {
		return "", err
	}
	return "/usr/bin/" + file, nil
}

func commandKey(name string, args ...string) string {
	return name + "|" + strings.Join(args, "|")
}

func TestSortIssuesPrefersActionableWorkOverEpic(t *testing.T) {
	issues := []issue{
		{ID: "epic", IssueType: "epic", Priority: 0, CreatedAt: "2026-03-10T10:00:00Z"},
		{ID: "task", IssueType: "task", Priority: 0, CreatedAt: "2026-03-10T11:00:00Z"},
	}

	sortIssues(issues)

	if issues[0].ID != "task" {
		t.Fatalf("expected task first, got %s", issues[0].ID)
	}
}

func TestBuildRP1PromptIncludesBeadsContext(t *testing.T) {
	prompt := buildRP1Prompt("/Users/felipeh/Development/jobber", issue{
		ID:                 "jobber-t6m.7",
		Title:              "Replace the web dev-server container with a production web build",
		Description:        "The current web container runs the Vite development server instead of serving a production build.",
		AcceptanceCriteria: "The web production container serves compiled assets only.",
		Parent:             "jobber-t6m",
	})

	checks := []string{
		"/rp1-build",
		"jobber-t6m-7-replace-the-web-dev-server-container-with-a-production-web",
		"beads://jobber/jobber-t6m.7",
		"bd show jobber-t6m.7 --json --long",
		"--afk",
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("expected prompt to contain %q\nprompt: %s", check, prompt)
		}
	}
}

func TestRunNextDryRunPrintsLaunchCommand(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	readyJSON := `[{"id":"jobber-1","title":"Fix bug","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`
	showJSON := `[{"id":"jobber-1","title":"Fix bug","description":"Bug details","acceptance_criteria":"Works","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`

	fake := &fakeRunner{
		runOutputs: map[string][]byte{
			commandKey("bd", "ready", "--json"):                      []byte(readyJSON),
			commandKey("bd", "show", "jobber-1", "--json", "--long"): []byte(showJSON),
		},
		lookups: map[string]error{},
	}

	var stdout bytes.Buffer
	if err := run([]string{"next", "--repo", repo, "--dry-run"}, strings.NewReader(""), &stdout, &stdout, fake); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "command: opencode") {
		t.Fatalf("expected dry-run output to include launch command, got: %s", out)
	}
	if len(fake.started) != 0 {
		t.Fatalf("expected no started commands during dry-run")
	}
}

func TestRunNextPrintPromptOnly(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	readyJSON := `[{"id":"jobber-1","title":"Fix bug","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`
	showJSON := `[{"id":"jobber-1","title":"Fix bug","description":"Bug details","acceptance_criteria":"Works","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`

	fake := &fakeRunner{
		runOutputs: map[string][]byte{
			commandKey("bd", "ready", "--json"):                      []byte(readyJSON),
			commandKey("bd", "show", "jobber-1", "--json", "--long"): []byte(showJSON),
		},
		lookups: map[string]error{},
	}

	var stdout bytes.Buffer
	if err := run([]string{"next", "--repo", repo, "--print-prompt"}, strings.NewReader(""), &stdout, &stdout, fake); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "/rp1-build") {
		t.Fatalf("expected prompt output, got: %s", stdout.String())
	}
	if len(fake.started) != 0 {
		t.Fatalf("expected no launcher start when printing prompt")
	}
	for _, call := range fake.runs {
		if call.name == "bd" && len(call.args) > 0 && call.args[0] == "update" {
			t.Fatalf("expected no claim when printing prompt")
		}
	}
}

func TestParseNextArgsReadsConfigDefaults(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	configJSON := `{
		"repo": "/tmp/jobber",
		"launcher": "claude",
		"model": "opus",
		"no_claim": true
	}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := parseNextArgs([]string{"--config", configPath})
	if err != nil {
		t.Fatalf("parseNextArgs failed: %v", err)
	}

	if cfg.RepoPath != "/tmp/jobber" {
		t.Fatalf("expected repo from config, got %q", cfg.RepoPath)
	}
	if cfg.Launcher != "claude" {
		t.Fatalf("expected claude launcher, got %q", cfg.Launcher)
	}
	if cfg.Model != "opus" {
		t.Fatalf("expected model from config, got %q", cfg.Model)
	}
	if !cfg.NoClaim {
		t.Fatalf("expected no-claim from config")
	}
}

func TestParseNextArgsCliOverridesConfig(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	configJSON := `{"repo":"/tmp/jobber","launcher":"claude","model":"opus","agent":"ignored-agent"}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := parseNextArgs([]string{"--config", configPath, "--repo", "/tmp/other", "--launcher", "opencode", "--model", "anthropic/custom", "--agent", "custom-agent"})
	if err != nil {
		t.Fatalf("parseNextArgs failed: %v", err)
	}

	if cfg.RepoPath != "/tmp/other" || cfg.Launcher != "opencode" || cfg.Model != "anthropic/custom" || cfg.Agent != "custom-agent" {
		t.Fatalf("expected CLI values to win, got %#v", cfg)
	}
}

func TestRunNextLaunchesClaudeWhenRequested(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	readyJSON := `[{"id":"jobber-1","title":"Fix bug","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`
	showJSON := `[{"id":"jobber-1","title":"Fix bug","description":"Bug details","acceptance_criteria":"Works","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`

	fake := &fakeRunner{
		runOutputs: map[string][]byte{
			commandKey("bd", "ready", "--json"):                      []byte(readyJSON),
			commandKey("bd", "show", "jobber-1", "--json", "--long"): []byte(showJSON),
		},
		lookups: map[string]error{},
	}

	var stdout bytes.Buffer
	if err := run([]string{"next", "--repo", repo, "--launcher", "claude", "--no-claim"}, strings.NewReader(""), &stdout, &stdout, fake); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if len(fake.started) != 1 {
		t.Fatalf("expected one launch, got %d", len(fake.started))
	}
	if fake.started[0].name != "claude" {
		t.Fatalf("expected claude launch, got %s", fake.started[0].name)
	}
	if !containsArg(fake.started[0].args, "--model", defaultClaudeMode) {
		t.Fatalf("expected claude default model in args: %#v", fake.started[0].args)
	}
	if containsFlag(fake.started[0].args, "--agent") {
		t.Fatalf("did not expect --agent for claude launch: %#v", fake.started[0].args)
	}
}

func TestRunNextClaimsAndLaunchesSelectedIssue(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	readyJSON := `[
		{"id":"jobber-epic","title":"Epic","priority":0,"issue_type":"epic","created_at":"2026-03-10T09:00:00Z"},
		{"id":"jobber-task","title":"Task","priority":0,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}
	]`
	showJSON := `[{"id":"jobber-task","title":"Task","description":"Task details","acceptance_criteria":"Done","priority":0,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`

	fake := &fakeRunner{
		runOutputs: map[string][]byte{
			commandKey("bd", "ready", "--json"):                            []byte(readyJSON),
			commandKey("bd", "show", "jobber-task", "--json", "--long"):    []byte(showJSON),
			commandKey("bd", "update", "jobber-task", "--claim", "--json"): []byte(`{"id":"jobber-task"}`),
		},
		lookups: map[string]error{},
	}

	var stdout bytes.Buffer
	if err := run([]string{"next", "--repo", repo}, strings.NewReader(""), &stdout, &stdout, fake); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if len(fake.started) != 1 {
		t.Fatalf("expected one launch, got %d", len(fake.started))
	}
	if fake.started[0].name != "opencode" {
		t.Fatalf("expected opencode launch, got %s", fake.started[0].name)
	}
	if !containsArg(fake.started[0].args, "--agent", defaultAgent) {
		t.Fatalf("expected agent %s in launch args: %#v", defaultAgent, fake.started[0].args)
	}
	if !containsArg(fake.started[0].args, "--model", defaultModel) {
		t.Fatalf("expected model %s in launch args: %#v", defaultModel, fake.started[0].args)
	}
	if !strings.Contains(strings.Join(fake.started[0].args, " "), "jobber-task") {
		t.Fatalf("expected selected task in prompt args: %#v", fake.started[0].args)
	}
}

func TestRunReturnsHelpfulErrorWhenBinaryMissing(t *testing.T) {
	fake := &fakeRunner{
		lookups: map[string]error{"bd": errors.New("missing")},
	}

	err := run([]string{"next"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, fake)
	if err == nil || !strings.Contains(err.Error(), "bd not found") {
		t.Fatalf("expected missing bd error, got %v", err)
	}
}

func TestVersionCommandPrintsVersion(t *testing.T) {
	oldVersion := version
	oldRef := ref
	version = "0.1.0"
	ref = "abc123"
	defer func() {
		version = oldVersion
		ref = oldRef
	}()

	var stdout bytes.Buffer
	if err := run([]string{"version"}, strings.NewReader(""), &stdout, &stdout, &fakeRunner{lookups: map[string]error{}}); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if strings.TrimSpace(stdout.String()) != "0.1.0 (abc123)" {
		t.Fatalf("unexpected version output: %q", stdout.String())
	}
}

func TestRunLoopProcessesIssuesUntilEmpty(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	readyJSONFirst := `[{"id":"job-1","title":"First task","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`
	readyJSONSecond := `[{"id":"job-2","title":"Second task","priority":2,"issue_type":"task","created_at":"2026-03-10T11:00:00Z"}]`
	showJSON1 := `[{"id":"job-1","title":"First task","description":"Details","acceptance_criteria":"Done","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`
	showJSON2 := `[{"id":"job-2","title":"Second task","description":"Details","acceptance_criteria":"Done","priority":2,"issue_type":"task","created_at":"2026-03-10T11:00:00Z"}]`

	// Track how many times bd ready is called to return different results.
	readyCallCount := 0
	fake := &fakeRunner{
		runOutputs: map[string][]byte{
			commandKey("bd", "show", "job-1", "--json", "--long"):    []byte(showJSON1),
			commandKey("bd", "show", "job-2", "--json", "--long"):    []byte(showJSON2),
			commandKey("bd", "update", "job-1", "--claim", "--json"): []byte(`{"id":"job-1"}`),
			commandKey("bd", "update", "job-2", "--claim", "--json"): []byte(`{"id":"job-2"}`),
		},
		lookups: map[string]error{},
	}

	// Override Run to cycle through ready results.
	origRun := fake.Run
	_ = origRun
	readyKey := commandKey("bd", "ready", "--json")
	fake.runOutputs[readyKey] = []byte(readyJSONFirst)

	// We need a custom runner to cycle through ready outputs and match close commands.
	cycleRunner := &cycleReadyRunner{
		fakeRunner: fake,
		readyOutputs: [][]byte{
			[]byte(readyJSONFirst),
			[]byte(readyJSONSecond),
			[]byte(`[]`),
		},
	}

	var stdout, stderr bytes.Buffer
	if err := runLoop(loopConfig{
		RepoPath: repo,
		Launcher: "opencode",
		Model:    defaultModel,
		Agent:    defaultAgent,
		Cooldown: 0,
	}, strings.NewReader(""), &stdout, &stderr, cycleRunner); err != nil {
		t.Fatalf("runLoop failed: %v\nstderr: %s", err, stderr.String())
	}

	if len(cycleRunner.fakeRunner.started) != 2 {
		t.Fatalf("expected 2 launches, got %d", len(cycleRunner.fakeRunner.started))
	}
	if !strings.Contains(stderr.String(), "closed job-1") {
		t.Fatalf("expected job-1 to be closed, stderr: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "closed job-2") {
		t.Fatalf("expected job-2 to be closed, stderr: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "2 completed, 0 failed") {
		t.Fatalf("expected completion summary, stderr: %s", stderr.String())
	}
	_ = readyCallCount
}

func TestRunLoopRespectsMaxTasks(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	readyJSON := `[{"id":"job-1","title":"Task","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`
	showJSON := `[{"id":"job-1","title":"Task","description":"Details","acceptance_criteria":"Done","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`

	cycleRunner := &cycleReadyRunner{
		fakeRunner: &fakeRunner{
			runOutputs: map[string][]byte{
				commandKey("bd", "show", "job-1", "--json", "--long"):    []byte(showJSON),
				commandKey("bd", "update", "job-1", "--claim", "--json"): []byte(`{"id":"job-1"}`),
			},
			lookups: map[string]error{},
		},
		readyOutputs: [][]byte{
			[]byte(readyJSON),
			[]byte(readyJSON),
			[]byte(readyJSON),
		},
	}

	var stdout, stderr bytes.Buffer
	if err := runLoop(loopConfig{
		RepoPath: repo,
		Launcher: "opencode",
		Model:    defaultModel,
		Agent:    defaultAgent,
		Cooldown: 0,
		MaxTasks: 1,
	}, strings.NewReader(""), &stdout, &stderr, cycleRunner); err != nil {
		t.Fatalf("runLoop failed: %v", err)
	}

	if len(cycleRunner.fakeRunner.started) != 1 {
		t.Fatalf("expected 1 launch with max-tasks=1, got %d", len(cycleRunner.fakeRunner.started))
	}
	if !strings.Contains(stderr.String(), "reached max-tasks limit") {
		t.Fatalf("expected max-tasks log message, stderr: %s", stderr.String())
	}
}

func TestRunLoopContinuesOnLaunchFailure(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	showJSON1 := `[{"id":"job-1","title":"Fails","description":"Details","acceptance_criteria":"Done","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`
	showJSON2 := `[{"id":"job-2","title":"Works","description":"Details","acceptance_criteria":"Done","priority":2,"issue_type":"task","created_at":"2026-03-10T11:00:00Z"}]`

	cycleRunner := &cycleReadyRunner{
		fakeRunner: &fakeRunner{
			runOutputs: map[string][]byte{
				commandKey("bd", "show", "job-1", "--json", "--long"):    []byte(showJSON1),
				commandKey("bd", "show", "job-2", "--json", "--long"):    []byte(showJSON2),
				commandKey("bd", "update", "job-1", "--claim", "--json"): []byte(`{"id":"job-1"}`),
				commandKey("bd", "update", "job-2", "--claim", "--json"): []byte(`{"id":"job-2"}`),
			},
			lookups: map[string]error{},
		},
		readyOutputs: [][]byte{
			[]byte(`[{"id":"job-1","title":"Fails","priority":1,"issue_type":"task","created_at":"2026-03-10T10:00:00Z"}]`),
			[]byte(`[{"id":"job-2","title":"Works","priority":2,"issue_type":"task","created_at":"2026-03-10T11:00:00Z"}]`),
			[]byte(`[]`),
		},
		startErrors: map[int]error{0: errors.New("agent crashed")},
	}

	var stdout, stderr bytes.Buffer
	if err := runLoop(loopConfig{
		RepoPath: repo,
		Launcher: "opencode",
		Model:    defaultModel,
		Agent:    defaultAgent,
		Cooldown: 0,
	}, strings.NewReader(""), &stdout, &stderr, cycleRunner); err != nil {
		t.Fatalf("runLoop failed: %v", err)
	}

	if !strings.Contains(stderr.String(), "job-1 failed") {
		t.Fatalf("expected failure log for job-1, stderr: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "closed job-2") {
		t.Fatalf("expected job-2 to be closed after job-1 failure, stderr: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "1 completed, 1 failed") {
		t.Fatalf("expected summary with 1 completed 1 failed, stderr: %s", stderr.String())
	}
}

// cycleReadyRunner wraps fakeRunner but cycles through different bd ready outputs
// and optionally fails specific Start calls.
type cycleReadyRunner struct {
	*fakeRunner
	readyOutputs [][]byte
	readyIndex   int
	startErrors  map[int]error
	startIndex   int
}

func (c *cycleReadyRunner) Run(dir string, name string, args ...string) ([]byte, error) {
	key := commandKey(name, args...)
	readyKey := commandKey("bd", "ready", "--json")

	if key == readyKey {
		idx := c.readyIndex
		if idx >= len(c.readyOutputs) {
			return []byte(`[]`), nil
		}
		c.readyIndex++
		c.fakeRunner.runs = append(c.fakeRunner.runs, invocation{dir: dir, name: name, args: append([]string{}, args...)})
		return c.readyOutputs[idx], nil
	}

	// Match bd close commands dynamically (any reason string).
	if name == "bd" && len(args) >= 1 && args[0] == "close" {
		c.fakeRunner.runs = append(c.fakeRunner.runs, invocation{dir: dir, name: name, args: append([]string{}, args...)})
		return []byte(`{}`), nil
	}

	return c.fakeRunner.Run(dir, name, args...)
}

func (c *cycleReadyRunner) Start(dir string, stdin io.Reader, stdout io.Writer, stderr io.Writer, name string, args ...string) error {
	idx := c.startIndex
	c.startIndex++
	c.fakeRunner.started = append(c.fakeRunner.started, invocation{dir: dir, name: name, args: append([]string{}, args...)})
	if c.startErrors != nil {
		if err := c.startErrors[idx]; err != nil {
			return err
		}
	}
	return nil
}

func (c *cycleReadyRunner) LookPath(file string) (string, error) {
	return c.fakeRunner.LookPath(file)
}

func containsArg(args []string, flag string, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func containsFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

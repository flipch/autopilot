package autopilot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultLauncher    = "opencode"
	defaultModel       = "anthropic/claude-opus-4-6"
	defaultClaudeMode  = "opus"
	defaultAgent       = "opencoder"
	defaultClaudeEffort = "max"
	maxParallel        = 5
)

var errUsage = errors.New("usage")

var (
	version = "dev"
	ref     = "none"
)

type issue struct {
	ID                 string  `json:"id"`
	Title              string  `json:"title"`
	Description        string  `json:"description"`
	AcceptanceCriteria string  `json:"acceptance_criteria"`
	Priority           int     `json:"priority"`
	IssueType          string  `json:"issue_type"`
	Parent             string  `json:"parent"`
	CreatedAt          string  `json:"created_at"`
	Dependencies       []issue `json:"dependencies"`
	Dependents         []issue `json:"dependents"`
}

type config struct {
	RepoPath    string
	IssueID     string
	Launcher    string
	Model       string
	Agent       string
	Effort      string
	Print       bool // use --print mode (non-interactive, exits when done)
	DryRun      bool
	PrintPrompt bool
	Pick        bool
	NoClaim     bool
	List        bool
	Config      string
}

type fileConfig struct {
	RepoPath string     `json:"repo"`
	Launcher string     `json:"launcher"`
	Model    string     `json:"model"`
	Agent    string     `json:"agent"`
	Effort   string     `json:"effort"`
	NoClaim  bool       `json:"no_claim"`
	Roles    roleConfig `json:"roles"`
}

// roleConfig allows per-role model/effort overrides.
type roleConfig struct {
	Builder  roleOverride `json:"builder"`
	Reviewer roleOverride `json:"reviewer"`
	Fixer    roleOverride `json:"fixer"`
}

type roleOverride struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type runner interface {
	Run(dir string, name string, args ...string) ([]byte, error)
	Start(dir string, stdin io.Reader, stdout io.Writer, stderr io.Writer, name string, args ...string) error
	LookPath(file string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s failed: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (execRunner) Start(dir string, stdin io.Reader, stdout io.Writer, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Strip env vars that prevent nested Claude sessions.
	cmd.Env = filteredEnv("CLAUDECODE", "CLAUDE_CODE")
	return cmd.Run()
}

// filteredEnv returns os.Environ() with the named variables removed and
// autopilot-specific env vars injected for rp1 integration.
func filteredEnv(names ...string) []string {
	skip := make(map[string]bool, len(names))
	for _, n := range names {
		skip[n] = true
	}
	// Also strip vars we inject below to avoid duplicates.
	skip["RP1_PR_REVIEW_VERDICT"] = true
	skip["RP1_PR_REVIEW_ADD_COMMENTS"] = true

	var env []string
	for _, e := range os.Environ() {
		key := e[:strings.IndexByte(e, '=')]
		if !skip[key] {
			env = append(env, e)
		}
	}
	// Ensure /pr-review posts a GitHub review so autopilot can read the verdict.
	// CI=true triggers rp1's comment posting mode (P5).
	env = append(env, "RP1_PR_REVIEW_VERDICT=auto", "RP1_PR_REVIEW_ADD_COMMENTS=true", "CI=true")
	return env
}

func (execRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	return run(args, stdin, stdout, stderr, execRunner{})
}

type loopConfig struct {
	RepoPath        string
	Launcher        string
	Model           string
	Agent           string
	Effort          string
	Cooldown        time.Duration
	MaxTasks        int
	Parallel        int
	Review          bool
	MaxReviewRounds int
	LogFile         string
	Zellij          bool
	Config          string
	roles           roleConfig // from file config, used by review cycle
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, cmd runner) error {
	if len(args) == 0 {
		return errors.New("usage: autopilot <next|loop|version> [flags]")
	}

	switch args[0] {
	case "next":
		cfg, err := parseNextArgs(args[1:])
		if err != nil {
			return err
		}
		return runNext(cfg, stdin, stdout, stderr, cmd)
	case "loop":
		cfg, err := parseLoopArgs(args[1:])
		if err != nil {
			return err
		}
		return runLoop(cfg, stdin, stdout, stderr, cmd)
	case "version":
		_, err := fmt.Fprintf(stdout, "%s (%s)\n", version, ref)
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func parseNextArgs(args []string) (config, error) {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultConfigPath, err := defaultConfigPath()
	if err != nil {
		return config{}, err
	}

	var cfg config
	fs.StringVar(&cfg.RepoPath, "repo", ".", "target repository path")
	fs.StringVar(&cfg.IssueID, "issue", "", "specific beads issue id to use")
	fs.StringVar(&cfg.Launcher, "launcher", "", "launcher to use: opencode or claude")
	fs.StringVar(&cfg.Model, "model", defaultModel, "OpenCode model id")
	fs.StringVar(&cfg.Agent, "agent", defaultAgent, "OpenCode agent id")
	fs.StringVar(&cfg.Effort, "effort", defaultClaudeEffort, "Claude thinking effort (low, medium, high, max)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print selected issue and launch command without executing")
	fs.BoolVar(&cfg.PrintPrompt, "print-prompt", false, "print the generated /rp1-build prompt and exit")
	fs.BoolVar(&cfg.Pick, "pick", false, "interactively pick from ready issues")
	fs.BoolVar(&cfg.NoClaim, "no-claim", false, "do not claim the issue before launch")
	fs.BoolVar(&cfg.List, "list", false, "list ready issues and exit")
	fs.StringVar(&cfg.Config, "config", defaultConfigPath, "config file path")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	merged, err := mergeConfig(cfg)
	if err != nil {
		return config{}, err
	}

	if merged.Launcher == "" {
		merged.Launcher = defaultLauncher
	}
	if merged.Model == "" {
		merged.Model = defaultModel
	}
	if merged.Agent == "" {
		merged.Agent = defaultAgent
	}

	if err := validateLauncher(merged.Launcher); err != nil {
		return config{}, err
	}

	if merged.Launcher == "claude" && merged.Model == defaultModel {
		merged.Model = defaultClaudeMode
	}

	if merged.Launcher == "claude" {
		merged.Agent = ""
	}

	if merged.RepoPath == "" {
		merged.RepoPath = "."
	}

	return merged, nil
}

func parseLoopArgs(args []string) (loopConfig, error) {
	fs := flag.NewFlagSet("loop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	defaultCfgPath, err := defaultConfigPath()
	if err != nil {
		return loopConfig{}, err
	}

	var cfg loopConfig
	fs.StringVar(&cfg.RepoPath, "repo", ".", "target repository path")
	fs.StringVar(&cfg.Launcher, "launcher", "", "launcher to use: opencode or claude")
	fs.StringVar(&cfg.Model, "model", defaultModel, "model id")
	fs.StringVar(&cfg.Agent, "agent", defaultAgent, "agent id (opencode only)")
	fs.StringVar(&cfg.Effort, "effort", defaultClaudeEffort, "Claude thinking effort (low, medium, high, max)")
	fs.DurationVar(&cfg.Cooldown, "cooldown", 10*time.Second, "pause between tasks")
	fs.IntVar(&cfg.MaxTasks, "max-tasks", 0, "maximum tasks to process (0 = unlimited)")
	fs.IntVar(&cfg.Parallel, "parallel", 0, "number of parallel workers (0 = auto-detect from ready issues, max 5)")
	fs.BoolVar(&cfg.Review, "review", false, "enable PR review cycle (creates PR, reviews, fixes feedback, merges)")
	fs.IntVar(&cfg.MaxReviewRounds, "max-review-rounds", 3, "maximum review/fix iterations per PR")
	fs.StringVar(&cfg.LogFile, "log-file", "", "write structured logs to file (in addition to stderr)")
	fs.BoolVar(&cfg.Zellij, "zellij", false, "spawn each worker in a zellij pane (visible, interactive)")
	fs.StringVar(&cfg.Config, "config", defaultCfgPath, "config file path")

	if err := fs.Parse(args); err != nil {
		return loopConfig{}, err
	}

	// Merge file config for defaults.
	fileCfg, err := loadFileConfig(cfg.Config)
	if err != nil {
		return loopConfig{}, err
	}
	if cfg.RepoPath == "." && fileCfg.RepoPath != "" {
		cfg.RepoPath = fileCfg.RepoPath
	}
	if cfg.Launcher == "" && fileCfg.Launcher != "" {
		cfg.Launcher = fileCfg.Launcher
	}
	if cfg.Model == defaultModel && fileCfg.Model != "" {
		cfg.Model = fileCfg.Model
	}
	if cfg.Agent == defaultAgent && fileCfg.Agent != "" {
		cfg.Agent = fileCfg.Agent
	}
	if cfg.Effort == defaultClaudeEffort && fileCfg.Effort != "" {
		cfg.Effort = fileCfg.Effort
	}

	if cfg.Launcher == "" {
		cfg.Launcher = defaultLauncher
	}
	if err := validateLauncher(cfg.Launcher); err != nil {
		return loopConfig{}, err
	}
	if cfg.Launcher == "claude" && cfg.Model == defaultModel {
		cfg.Model = defaultClaudeMode
	}
	if cfg.Launcher == "claude" {
		cfg.Agent = ""
	}
	if cfg.RepoPath == "" {
		cfg.RepoPath = "."
	}

	// Store role overrides from file config for use by review cycle.
	cfg.roles = fileCfg.Roles

	return cfg, nil
}

func mergeConfig(cli config) (config, error) {
	merged := cli

	fileCfg, err := loadFileConfig(cli.Config)
	if err != nil {
		return config{}, err
	}

	if cli.RepoPath == "." && fileCfg.RepoPath != "" {
		merged.RepoPath = fileCfg.RepoPath
	}
	if cli.Launcher == "" && fileCfg.Launcher != "" {
		merged.Launcher = fileCfg.Launcher
	}
	if cli.Model == defaultModel && fileCfg.Model != "" {
		merged.Model = fileCfg.Model
	}
	if cli.Agent == defaultAgent && fileCfg.Agent != "" {
		merged.Agent = fileCfg.Agent
	}
	if !cli.NoClaim && fileCfg.NoClaim {
		merged.NoClaim = true
	}

	return merged, nil
}

func loadFileConfig(path string) (fileConfig, error) {
	if path == "" {
		return fileConfig{}, nil
	}

	expanded, err := expandHome(path)
	if err != nil {
		return fileConfig{}, err
	}

	content, err := os.ReadFile(expanded)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileConfig{}, nil
		}
		return fileConfig{}, fmt.Errorf("read config: %w", err)
	}

	var cfg fileConfig
	if err := json.Unmarshal(content, &cfg); err != nil {
		return fileConfig{}, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

func defaultConfigPath() (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(usr.HomeDir, ".config", "autopilot", "config.json"), nil
}

func expandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}

	usr, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}

	if path == "~" {
		return usr.HomeDir, nil
	}

	if strings.HasPrefix(path, "~/") {
		return filepath.Join(usr.HomeDir, path[2:]), nil
	}

	return "", fmt.Errorf("unsupported home expansion path: %s", path)
}

func validateLauncher(value string) error {
	switch value {
	case "opencode", "claude":
		return nil
	default:
		return fmt.Errorf("unsupported launcher %q", value)
	}
}

func runNext(cfg config, stdin io.Reader, stdout io.Writer, stderr io.Writer, cmd runner) error {
	if _, err := cmd.LookPath("bd"); err != nil {
		return errors.New("bd not found in PATH")
	}
	launcherBinary := cfg.Launcher
	if _, err := cmd.LookPath(launcherBinary); err != nil {
		return fmt.Errorf("%s not found in PATH", launcherBinary)
	}

	repoRoot, err := resolveRepoRoot(cfg.RepoPath)
	if err != nil {
		return err
	}

	ready, err := loadReadyIssues(repoRoot, cmd)
	if err != nil {
		return err
	}
	if len(ready) == 0 {
		return fmt.Errorf("no ready beads issues found in %s", repoRoot)
	}

	if cfg.List {
		printIssues(stdout, ready)
		return nil
	}

	selected, err := selectIssue(cfg, ready, stdin, stdout, repoRoot, cmd)
	if err != nil {
		return err
	}

	prompt := buildRP1Prompt(repoRoot, selected, false)
	launchArgs, err := buildLaunchArgs(cfg, repoRoot, prompt)
	if err != nil {
		return err
	}

	if cfg.PrintPrompt {
		_, err := fmt.Fprintln(stdout, prompt)
		return err
	}

	if cfg.DryRun {
		fmt.Fprintf(stdout, "repo: %s\n", repoRoot)
		fmt.Fprintf(stdout, "issue: %s - %s\n", selected.ID, selected.Title)
		fmt.Fprintf(stdout, "launcher: %s\n", cfg.Launcher)
		fmt.Fprintf(stdout, "claim: %t\n", !cfg.NoClaim)
		fmt.Fprintf(stdout, "command: %s %s\n", cfg.Launcher, strings.Join(quoteArgs(launchArgs), " "))
		return nil
	}

	if !cfg.NoClaim {
		if _, err := cmd.Run(repoRoot, "bd", "update", selected.ID, "--claim", "--json"); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "claimed %s\n", selected.ID)
	}

	fmt.Fprintf(stdout, "launching %s for %s\n", cfg.Launcher, selected.ID)
	return cmd.Start(repoRoot, stdin, stdout, stderr, cfg.Launcher, launchArgs...)
}

func runLoop(cfg loopConfig, stdin io.Reader, stdout io.Writer, stderr io.Writer, cmd runner) error {
	logWriter := io.Writer(stderr)
	if cfg.LogFile != "" {
		expanded, err := expandHome(cfg.LogFile)
		if err != nil {
			return fmt.Errorf("expand log file path: %w", err)
		}
		f, err := os.OpenFile(expanded, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer f.Close()
		logWriter = io.MultiWriter(stderr, f)
	}
	logger := log.New(logWriter, "", log.Ldate|log.Ltime)

	if _, err := cmd.LookPath("bd"); err != nil {
		return errors.New("bd not found in PATH")
	}
	if _, err := cmd.LookPath(cfg.Launcher); err != nil {
		return fmt.Errorf("%s not found in PATH", cfg.Launcher)
	}
	if cfg.Review {
		if _, err := cmd.LookPath("gh"); err != nil {
			return errors.New("gh not found in PATH (required for --review)")
		}
	}

	repoRoot, err := resolveRepoRoot(cfg.RepoPath)
	if err != nil {
		return err
	}

	// Determine worker count.
	workerCount := cfg.Parallel
	if workerCount == 0 {
		ready, err := loadReadyIssues(repoRoot, cmd)
		if err != nil {
			return err
		}
		workerCount = len(ready)
		if workerCount > maxParallel {
			workerCount = maxParallel
		}
		if workerCount == 0 {
			logger.Printf("loop: no ready issues found")
			return nil
		}
	}

	repoName := filepath.Base(repoRoot)

	// Zellij mode: spawn each worker in its own zellij pane.
	if cfg.Zellij {
		if _, err := cmd.LookPath("zellij"); err != nil {
			return errors.New("zellij not found in PATH (required for --zellij)")
		}
		logger.Printf("loop: launching %d worker(s) in zellij for %s", workerCount, repoName)
		return launchZellij(cfg, repoRoot, workerCount, cmd)
	}

	logger.Printf("loop: starting %d worker(s) for %s (launcher=%s, review=%t, cooldown=%s)", workerCount, repoName, cfg.Launcher, cfg.Review, cfg.Cooldown)

	// Handle graceful shutdown — closing stopCh broadcasts to all workers.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	stopCh := make(chan struct{})
	go func() {
		sig := <-sigCh
		logger.Printf("loop: received %s, signaling all workers to stop", sig)
		close(stopCh)
	}()

	var completed, failed int64

	if workerCount == 1 {
		// Single worker — current behavior with inherited I/O.
		wLogger := log.New(logWriter, "", log.Ldate|log.Ltime)
		c, f := runWorker(cfg, repoRoot, stopCh, stdin, stdout, stderr, cmd, wLogger)
		completed, failed = int64(c), int64(f)
	} else {
		// Parallel workers — agent output goes to /dev/null, autopilot log is source of truth.
		var wg sync.WaitGroup
		for i := 1; i <= workerCount; i++ {
			wg.Add(1)
			workerID := i
			go func() {
				defer wg.Done()
				wLogger := log.New(logWriter, fmt.Sprintf("[w%d] ", workerID), log.Ldate|log.Ltime)
				c, f := runWorker(cfg, repoRoot, stopCh, strings.NewReader(""), io.Discard, io.Discard, cmd, wLogger)
				atomic.AddInt64(&completed, int64(c))
				atomic.AddInt64(&failed, int64(f))
			}()
		}
		wg.Wait()
	}

	logger.Printf("loop: done — %d completed, %d failed", completed, failed)
	return nil
}

// runWorker processes issues in a loop until no work remains or stop is signaled.
// Returns counts of completed and failed issues.
func runWorker(cfg loopConfig, repoRoot string, stopCh <-chan struct{}, stdin io.Reader, stdout io.Writer, stderr io.Writer, cmd runner, logger *log.Logger) (int, int) {
	completed := 0
	failed := 0

	for iteration := 1; ; iteration++ {
		// Check for stop signal.
		select {
		case <-stopCh:
			logger.Printf("stopping after %d completed, %d failed", completed, failed)
			return completed, failed
		default:
		}

		if cfg.MaxTasks > 0 && completed >= cfg.MaxTasks {
			logger.Printf("reached max-tasks limit (%d), stopping", cfg.MaxTasks)
			break
		}

		logger.Printf("iteration %d — checking for ready issues", iteration)

		ready, err := loadReadyIssues(repoRoot, cmd)
		if err != nil {
			logger.Printf("error loading ready issues: %v", err)
			break
		}
		if len(ready) == 0 {
			logger.Printf("no ready issues remaining (completed=%d, failed=%d)", completed, failed)
			break
		}

		// Try to claim any ready issue, skipping ones already taken.
		var full issue
		claimed := false
		for _, candidate := range ready {
			if _, err := cmd.Run(repoRoot, "bd", "update", candidate.ID, "--claim", "--json"); err != nil {
				logger.Printf("claim failed for %s (taken by another worker), trying next", candidate.ID)
				continue
			}
			full, err = loadIssue(repoRoot, candidate.ID, cmd)
			if err != nil {
				logger.Printf("error loading issue %s after claim: %v", candidate.ID, err)
				continue
			}
			claimed = true
			break
		}
		if !claimed {
			// All ready issues were taken by other workers. Back off and retry.
			logger.Printf("all %d ready issues claimed by other workers, backing off", len(ready))
			select {
			case <-stopCh:
				return completed, failed
			case <-time.After(5 * time.Second):
			}
			continue
		}
		logger.Printf("claimed %s — %s (priority=%d, type=%s)", full.ID, full.Title, full.Priority, full.IssueType)

		// Build and launch.
		prompt := buildRP1Prompt(repoRoot, full, cfg.Review)
		nextCfg := config{
			Launcher: cfg.Launcher,
			Model:    cfg.Model,
			Agent:    cfg.Agent,
			Effort:   cfg.Effort,
			Print:    true,
		}
		launchArgs, err := buildLaunchArgs(nextCfg, repoRoot, prompt)
		if err != nil {
			logger.Printf("error building launch args for %s: %v", full.ID, err)
			break
		}

		logger.Printf("launching %s for %s", cfg.Launcher, full.ID)
		startTime := time.Now()
		launchErr := cmd.Start(repoRoot, stdin, stdout, stderr, cfg.Launcher, launchArgs...)
		elapsed := time.Since(startTime).Truncate(time.Second)

		if launchErr != nil {
			failed++
			logger.Printf("%s build failed after %s — %v (completed=%d, failed=%d)", full.ID, elapsed, launchErr, completed, failed)
			goto cooldown
		}

		if cfg.Review {
			branchName := buildRequirementName(full)
			prNumber, prErr := detectPR(repoRoot, branchName, cmd)
			if prErr != nil {
				failed++
				logger.Printf("no PR found for %s (branch %s): %v", full.ID, branchName, prErr)
				goto cooldown
			}
			logger.Printf("detected PR #%d for %s", prNumber, full.ID)

			approved := false
			for round := 1; round <= cfg.MaxReviewRounds; round++ {
				logger.Printf("review round %d/%d for PR #%d (%s)", round, cfg.MaxReviewRounds, prNumber, full.ID)

				reviewPrompt := fmt.Sprintf("/pr-review %d", prNumber)
				if err := launchAgent(cfg, repoRoot, reviewPrompt, cfg.roles.Reviewer, stdin, stdout, stderr, cmd); err != nil {
					logger.Printf("review agent failed for PR #%d: %v", prNumber, err)
					break
				}

				verdict := detectVerdict(repoRoot, prNumber, cmd)
				logger.Printf("review verdict for PR #%d: %s", prNumber, verdict)

				if verdict == verdictApprove {
					approved = true
					break
				}
				if verdict == verdictBlock {
					logger.Printf("PR #%d blocked by review, skipping", prNumber)
					break
				}

				if round >= cfg.MaxReviewRounds {
					logger.Printf("exhausted %d review rounds for PR #%d", cfg.MaxReviewRounds, prNumber)
					break
				}

				logger.Printf("launching fix agent for PR #%d (round %d)", prNumber, round)
				fixPrompt := fmt.Sprintf("/address-pr-feedback %d --afk", prNumber)
				if err := launchAgent(cfg, repoRoot, fixPrompt, cfg.roles.Fixer, stdin, stdout, stderr, cmd); err != nil {
					logger.Printf("fix agent failed for PR #%d: %v", prNumber, err)
					break
				}

				// Push the fix — get the actual branch name from the PR since
				// rp1/claude may prefix it differently than our slug.
				pushBranch := branchName
				if prBranch, err := getPRBranch(repoRoot, prNumber, cmd); err == nil {
					pushBranch = prBranch
				}
				if _, pushErr := cmd.Run(repoRoot, "git", "push", "origin", pushBranch); pushErr != nil {
					logger.Printf("warning: git push failed for branch %s: %v", pushBranch, pushErr)
				}
			}

			if approved {
				if err := mergePR(repoRoot, prNumber, cmd); err != nil {
					failed++
					logger.Printf("failed to merge PR #%d: %v", prNumber, err)
				} else {
					logger.Printf("merged PR #%d", prNumber)
					closeReason := fmt.Sprintf("Completed by autopilot loop — PR #%d merged (launcher=%s, elapsed=%s)", prNumber, cfg.Launcher, time.Since(startTime).Truncate(time.Second))
					if _, err := cmd.Run(repoRoot, "bd", "close", full.ID, "--reason", closeReason, "--json"); err != nil {
						logger.Printf("warning: failed to close %s: %v", full.ID, err)
					} else {
						logger.Printf("closed %s", full.ID)
					}
					completed++
					logger.Printf("%s completed in %s (completed=%d, failed=%d)", full.ID, time.Since(startTime).Truncate(time.Second), completed, failed)
				}
			} else {
				failed++
				logger.Printf("%s not approved after review (completed=%d, failed=%d)", full.ID, completed, failed)
			}
		} else {
			reason := fmt.Sprintf("Completed by autopilot loop (launcher=%s, elapsed=%s)", cfg.Launcher, elapsed)
			if _, err := cmd.Run(repoRoot, "bd", "close", full.ID, "--reason", reason, "--json"); err != nil {
				logger.Printf("warning: failed to close %s: %v", full.ID, err)
			} else {
				logger.Printf("closed %s", full.ID)
			}
			completed++
			logger.Printf("%s completed in %s (completed=%d, failed=%d)", full.ID, elapsed, completed, failed)
		}

	cooldown:
		if cfg.Cooldown > 0 {
			logger.Printf("cooling down %s", cfg.Cooldown)
			select {
			case <-stopCh:
				logger.Printf("stop received during cooldown (completed=%d, failed=%d)", completed, failed)
				return completed, failed
			case <-time.After(cfg.Cooldown):
			}
		}
	}

	return completed, failed
}

// launchZellij spawns workers in zellij panes using a KDL layout.
// If already inside a zellij session, adds a new tab with the layout.
// Otherwise, creates a new zellij session.
func launchZellij(cfg loopConfig, repoRoot string, workerCount int, cmd runner) error {
	repoName := filepath.Base(repoRoot)
	workerArgs := buildWorkerArgs(cfg, repoRoot)

	layout := buildZellijLayout(workerCount, workerArgs)
	layoutDir := filepath.Join(repoRoot, ".autopilot")
	if err := os.MkdirAll(layoutDir, 0o755); err != nil {
		return fmt.Errorf("create .autopilot dir: %w", err)
	}
	layoutPath := filepath.Join(layoutDir, "zellij-layout.kdl")
	if err := os.WriteFile(layoutPath, []byte(layout), 0o644); err != nil {
		return fmt.Errorf("write layout file: %w", err)
	}

	zellijPath, err := cmd.LookPath("zellij")
	if err != nil {
		return fmt.Errorf("resolve zellij path: %w", err)
	}

	if os.Getenv("ZELLIJ") != "" {
		// Already in a zellij session — add workers as a new tab via --layout.
		_, err := cmd.Run(repoRoot, "zellij", "action", "new-tab", "--layout", layoutPath)
		return err
	}

	// Not in zellij — exec into a new session.
	sessionName := fmt.Sprintf("autopilot-%s", repoName)
	return syscall.Exec(zellijPath, []string{
		"zellij", "--session", sessionName, "--new-session-with-layout", layoutPath,
	}, filteredEnv("CLAUDECODE", "CLAUDE_CODE"))
}

// buildWorkerArgs constructs the autopilot command for a single-worker pane.
func buildWorkerArgs(cfg loopConfig, repoRoot string) []string {
	args := []string{"autopilot", "loop", "--parallel", "1", "--repo", repoRoot, "--launcher", cfg.Launcher}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.Agent != "" {
		args = append(args, "--agent", cfg.Agent)
	}
	if cfg.Effort != "" && cfg.Effort != defaultClaudeEffort {
		args = append(args, "--effort", cfg.Effort)
	}
	if cfg.Cooldown != 10*time.Second {
		args = append(args, "--cooldown", cfg.Cooldown.String())
	}
	if cfg.MaxTasks > 0 {
		args = append(args, "--max-tasks", fmt.Sprintf("%d", cfg.MaxTasks))
	}
	if cfg.Review {
		args = append(args, "--review")
		if cfg.MaxReviewRounds != 3 {
			args = append(args, "--max-review-rounds", fmt.Sprintf("%d", cfg.MaxReviewRounds))
		}
	}
	if cfg.LogFile != "" {
		args = append(args, "--log-file", cfg.LogFile)
	}
	return args
}

// buildZellijLayout generates a KDL layout with each worker in its own tab.
// Workers are staggered by 5 seconds each to prevent claim races on bd issues.
func buildZellijLayout(workerCount int, workerArgs []string) string {
	var buf bytes.Buffer
	buf.WriteString("layout {\n")

	for i := 1; i <= workerCount; i++ {
		delay := (i - 1) * 5
		buf.WriteString(fmt.Sprintf("    tab name=\"worker-%d\" {\n", i))
		buf.WriteString("        pane size=1 borderless=true {\n")
		buf.WriteString("            plugin location=\"tab-bar\"\n")
		buf.WriteString("        }\n")
		buf.WriteString(fmt.Sprintf("        pane name=\"worker-%d\" {\n", i))
		// Wrap with sleep to stagger starts and avoid claim races.
		buf.WriteString("            command \"sh\"\n")
		buf.WriteString("            args \"-c\"")
		if delay > 0 {
			buf.WriteString(fmt.Sprintf(" \"sleep %d && %s\"", delay, shellJoinArgs(workerArgs)))
		} else {
			buf.WriteString(fmt.Sprintf(" \"%s\"", shellJoinArgs(workerArgs)))
		}
		buf.WriteString("\n")
		buf.WriteString("        }\n")
		buf.WriteString("        pane size=1 borderless=true {\n")
		buf.WriteString("            plugin location=\"status-bar\"\n")
		buf.WriteString("        }\n")
		buf.WriteString("    }\n")
	}

	buf.WriteString("}\n")
	return buf.String()
}

// shellJoinArgs joins args into a shell-safe command string.
func shellJoinArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, " \t\n'\"\\$`") {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		} else {
			quoted[i] = arg
		}
	}
	return strings.Join(quoted, " ")
}

// launchAgent starts a short-lived agent session with the given prompt.
// Uses --print mode so the agent exits when done instead of waiting for input.
// An optional roleOverride can adjust model/effort for specific roles (reviewer, fixer).
func launchAgent(cfg loopConfig, repoRoot string, prompt string, role roleOverride, stdin io.Reader, stdout io.Writer, stderr io.Writer, cmd runner) error {
	agentCfg := config{
		Launcher: cfg.Launcher,
		Model:    cfg.Model,
		Agent:    cfg.Agent,
		Effort:   cfg.Effort,
		Print:    true,
	}
	if role.Model != "" {
		agentCfg.Model = role.Model
	}
	if role.Effort != "" {
		agentCfg.Effort = role.Effort
	}
	args, err := buildLaunchArgs(agentCfg, repoRoot, prompt)
	if err != nil {
		return err
	}
	return cmd.Start(repoRoot, stdin, stdout, stderr, cfg.Launcher, args...)
}

// detectPR finds an open PR for the given slug by trying multiple branch name
// patterns. rp1 and Claude Code use different prefixes (feature-, worktree-,
// quick-build-, or bare), so we try them all.
func detectPR(repoRoot string, slug string, cmd runner) (int, error) {
	candidates := []string{
		slug,
		"worktree-" + slug,
		"feature-" + slug,
		"worktree-feature-" + slug,
		"quick-build-" + slug,
	}

	for _, branch := range candidates {
		output, err := cmd.Run(repoRoot, "gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number", "--limit", "1")
		if err != nil {
			continue
		}
		var prs []struct {
			Number int `json:"number"`
		}
		if err := json.Unmarshal(output, &prs); err != nil || len(prs) == 0 {
			continue
		}
		return prs[0].Number, nil
	}

	return 0, fmt.Errorf("no open PR found for slug %s (tried %d branch patterns)", slug, len(candidates))
}

// Review verdict constants.
const (
	verdictApprove        = "approve"
	verdictRequestChanges = "request_changes"
	verdictBlock          = "block"
	verdictUnknown        = "unknown"
)

// detectVerdict checks for a review verdict, trying GitHub first, then the review file.
func detectVerdict(repoRoot string, prNumber int, cmd runner) string {
	// Primary: read the GitHub review decision set by /pr-review with RP1_PR_REVIEW_VERDICT=auto.
	output, err := cmd.Run(repoRoot, "gh", "pr", "view", fmt.Sprintf("%d", prNumber), "--json", "reviewDecision", "-q", ".reviewDecision")
	if err == nil {
		switch strings.TrimSpace(string(output)) {
		case "APPROVED":
			return verdictApprove
		case "CHANGES_REQUESTED":
			return verdictRequestChanges
		}
	}

	// Fallback: parse the review file.
	return parseReviewVerdict(repoRoot)
}

// parseReviewVerdict reads the latest pr-review report and extracts the verdict.
// It scans .rp1/work/pr-reviews/ for the most recently modified .md file and
// looks for structured verdict markers that /pr-review emits.
func parseReviewVerdict(repoRoot string) string {
	reviewDir := filepath.Join(repoRoot, ".rp1", "work", "pr-reviews")
	entries, err := os.ReadDir(reviewDir)
	if err != nil {
		return verdictUnknown
	}

	// Find the most recently modified .md file.
	var latestPath string
	var latestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if latestPath == "" || info.ModTime().After(latestMod) {
			latestPath = filepath.Join(reviewDir, e.Name())
			latestMod = info.ModTime()
		}
	}
	if latestPath == "" {
		return verdictUnknown
	}

	data, err := os.ReadFile(latestPath)
	if err != nil {
		return verdictUnknown
	}

	return extractVerdict(string(data))
}

// extractVerdict parses review content for verdict markers.
// Exported for testing via the package-level function.
func extractVerdict(content string) string {
	lower := strings.ToLower(content)

	// rp1 pr-review uses emoji markers and backtick-quoted verdicts.
	// Check structured patterns first (most reliable).
	verdictPatterns := []struct {
		verdict  string
		patterns []string
	}{
		{verdictApprove, []string{
			"verdict: approve", "verdict: `approve`",
			"judgment: approve", "judgment: `approve`",
			"decision: approve", "decision: `approve`",
			"\u2705 `approve`", "\u2705 approve",
		}},
		{verdictBlock, []string{
			"verdict: block", "verdict: `block`",
			"judgment: block", "judgment: `block`",
			"decision: block", "decision: `block`",
			"\U0001f6d1 `block`", "\U0001f6d1 block",
		}},
		{verdictRequestChanges, []string{
			"verdict: request_changes", "verdict: `request_changes`",
			"judgment: request_changes", "judgment: `request_changes`",
			"decision: request_changes", "decision: `request_changes`",
			"verdict: request changes", "judgment: request changes",
			"\u26a0\ufe0f `request_changes`", "\u26a0\ufe0f request_changes",
		}},
	}

	for _, vp := range verdictPatterns {
		for _, pattern := range vp.patterns {
			if strings.Contains(lower, pattern) {
				return vp.verdict
			}
		}
	}

	return verdictUnknown
}

// getPRBranch returns the head branch name of a PR.
func getPRBranch(repoRoot string, prNumber int, cmd runner) (string, error) {
	output, err := cmd.Run(repoRoot, "gh", "pr", "view", fmt.Sprintf("%d", prNumber), "--json", "headRefName", "-q", ".headRefName")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(output))
	if branch == "" {
		return "", fmt.Errorf("empty branch name for PR #%d", prNumber)
	}
	return branch, nil
}

// mergePR merges an open PR using squash strategy.
func mergePR(repoRoot string, prNumber int, cmd runner) error {
	_, err := cmd.Run(repoRoot, "gh", "pr", "merge", fmt.Sprintf("%d", prNumber), "--squash", "--delete-branch")
	return err
}

func buildLaunchArgs(cfg config, repoRoot string, prompt string) ([]string, error) {
	switch cfg.Launcher {
	case "opencode":
		args := []string{"--model", cfg.Model, "--prompt", prompt}
		if cfg.Agent != "" {
			args = append(args, "--agent", cfg.Agent)
		}
		args = append(args, repoRoot)
		return args, nil
	case "claude":
		args := []string{"--model", cfg.Model, "--dangerously-skip-permissions", "--effort", cfg.Effort}
		if cfg.Print {
			args = append(args, "--print")
		}
		args = append(args, prompt)
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported launcher %q", cfg.Launcher)
	}
}

func resolveRepoRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat repo path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repo path is not a directory: %s", abs)
	}

	if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
		return abs, nil
	}

	current := abs
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no git repository found for path %s", abs)
		}
		current = parent
	}
}

func loadReadyIssues(repoRoot string, cmd runner) ([]issue, error) {
	output, err := cmd.Run(repoRoot, "bd", "ready", "--json")
	if err != nil {
		return nil, err
	}

	var all []issue
	if err := json.Unmarshal(output, &all); err != nil {
		return nil, fmt.Errorf("parse bd ready output: %w", err)
	}

	// Filter out epics — they're parent trackers, not buildable tasks.
	issues := make([]issue, 0, len(all))
	for _, item := range all {
		if strings.EqualFold(item.IssueType, "epic") {
			continue
		}
		issues = append(issues, item)
	}

	sortIssues(issues)
	return issues, nil
}

func selectIssue(cfg config, ready []issue, stdin io.Reader, stdout io.Writer, repoRoot string, cmd runner) (issue, error) {
	if cfg.IssueID != "" {
		return loadIssue(repoRoot, cfg.IssueID, cmd)
	}

	if cfg.Pick {
		chosen, err := pickIssue(ready, stdin, stdout)
		if err != nil {
			return issue{}, err
		}
		return loadIssue(repoRoot, chosen.ID, cmd)
	}

	return loadIssue(repoRoot, ready[0].ID, cmd)
}

func loadIssue(repoRoot string, issueID string, cmd runner) (issue, error) {
	output, err := cmd.Run(repoRoot, "bd", "show", issueID, "--json", "--long")
	if err != nil {
		return issue{}, err
	}

	var issues []issue
	if err := json.Unmarshal(output, &issues); err != nil {
		return issue{}, fmt.Errorf("parse bd show output: %w", err)
	}
	if len(issues) == 0 {
		return issue{}, fmt.Errorf("issue %s not found", issueID)
	}

	return issues[0], nil
}

func sortIssues(issues []issue) {
	sort.SliceStable(issues, func(i int, j int) bool {
		left := issues[i]
		right := issues[j]

		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		if issueTypeRank(left.IssueType) != issueTypeRank(right.IssueType) {
			return issueTypeRank(left.IssueType) < issueTypeRank(right.IssueType)
		}

		leftTime := parseTime(left.CreatedAt)
		rightTime := parseTime(right.CreatedAt)
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}

		return left.ID < right.ID
	})
}

func issueTypeRank(issueType string) int {
	switch strings.ToLower(issueType) {
	case "bug":
		return 0
	case "task":
		return 1
	case "feature":
		return 2
	case "chore":
		return 3
	case "epic":
		return 4
	default:
		return 5
	}
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func pickIssue(issues []issue, stdin io.Reader, stdout io.Writer) (issue, error) {
	printIssues(stdout, issues)
	fmt.Fprint(stdout, "select issue number: ")

	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return issue{}, fmt.Errorf("read selection: %w", err)
	}

	line = strings.TrimSpace(line)
	if line == "" {
		return issue{}, errors.New("no selection provided")
	}

	var index int
	if _, err := fmt.Sscanf(line, "%d", &index); err != nil {
		return issue{}, fmt.Errorf("invalid selection %q", line)
	}
	if index < 1 || index > len(issues) {
		return issue{}, fmt.Errorf("selection out of range: %d", index)
	}

	return issues[index-1], nil
}

func printIssues(out io.Writer, issues []issue) {
	for i, item := range issues {
		fmt.Fprintf(out, "%d. [p%d] %s (%s) - %s\n", i+1, item.Priority, item.ID, item.IssueType, item.Title)
	}
}

func buildRP1Prompt(repoRoot string, item issue, gitPR bool) string {
	requirement := buildRequirementName(item)
	description := buildRequirementDescription(repoRoot, item)
	flags := "--git-worktree --afk"
	if gitPR {
		flags = "--git-worktree --git-pr --afk"
	}
	return fmt.Sprintf(`/rp1-build %s %s %s`, quoteSlashArg(requirement), quoteSlashArg(description), flags)
}

func buildRequirementName(item issue) string {
	reSlug := regexp.MustCompile(`[^a-z0-9]+`)
	seed := strings.ToLower(item.ID + "-" + item.Title)
	seed = reSlug.ReplaceAllString(seed, "-")
	seed = strings.Trim(seed, "-")
	if len(seed) > 72 {
		seed = strings.Trim(seed[:72], "-")
	}
	if seed == "" {
		return "autopilot-requirement"
	}
	return seed
}

func buildRequirementDescription(repoRoot string, item issue) string {
	repoName := filepath.Base(repoRoot)
	link := fmt.Sprintf("beads://%s/%s", repoName, item.ID)
	showCommand := fmt.Sprintf("cd %s && bd show %s --json --long", repoRoot, item.ID)
	showCommand = fmt.Sprintf("cd %s && bd show %s --json --long", quoteShell(repoRoot), item.ID)

	parts := []string{
		fmt.Sprintf("Autopilot-selected beads task %s for repo %s.", item.ID, repoName),
		fmt.Sprintf("Reference link: %s.", link),
		fmt.Sprintf("Inspect with: %s.", showCommand),
		fmt.Sprintf("Issue title: %s.", normalizeSentence(item.Title)),
	}

	if item.Parent != "" {
		parts = append(parts, fmt.Sprintf("Parent issue: %s.", item.Parent))
	}

	problem := shrinkText(item.Description, 700)
	if problem != "" {
		parts = append(parts, fmt.Sprintf("Issue context: %s.", problem))
	}

	acceptance := shrinkText(item.AcceptanceCriteria, 300)
	if acceptance != "" {
		parts = append(parts, fmt.Sprintf("Acceptance criteria: %s.", acceptance))
	}

	parts = append(parts, "Use the beads issue as the source of truth, update the relevant issue state, and work inside the target repository.")

	return strings.Join(parts, " ")
}

func normalizeSentence(value string) string {
	value = strings.TrimSpace(collapseWhitespace(value))
	value = strings.TrimSuffix(value, ".")
	return value
}

func shrinkText(value string, limit int) string {
	value = collapseWhitespace(stripMarkdown(value))
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	truncated := value[:limit]
	lastSpace := strings.LastIndex(truncated, " ")
	if lastSpace > limit/2 {
		truncated = truncated[:lastSpace]
	}
	return strings.TrimSpace(truncated) + "…"
}

func stripMarkdown(value string) string {
	replacer := strings.NewReplacer("#", " ", "`", " ", "*", " ", "_", " ")
	value = replacer.Replace(value)
	value = strings.ReplaceAll(value, "- ", " ")
	return value
}

func collapseWhitespace(value string) string {
	fields := strings.Fields(value)
	return strings.Join(fields, " ")
}

func quoteSlashArg(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, `"`, `\"`)
	return fmt.Sprintf(`"%s"`, value)
}

func quoteArgs(args []string) []string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.ContainsAny(arg, " \t\n\"'") {
			quoted = append(quoted, quoteShell(arg))
			continue
		}
		quoted = append(quoted, arg)
	}
	return quoted
}

func quoteShell(value string) string {
	var buffer bytes.Buffer
	buffer.WriteByte('"')
	for _, r := range value {
		if r == '\\' || r == '"' {
			buffer.WriteByte('\\')
		}
		buffer.WriteRune(r)
	}
	buffer.WriteByte('"')
	return buffer.String()
}

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
	"syscall"
	"time"
)

const (
	defaultLauncher   = "opencode"
	defaultModel      = "anthropic/claude-opus-4-6"
	defaultClaudeMode = "opus"
	defaultAgent      = "opencoder"
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
	DryRun      bool
	PrintPrompt bool
	Pick        bool
	NoClaim     bool
	List        bool
	Config      string
}

type fileConfig struct {
	RepoPath string `json:"repo"`
	Launcher string `json:"launcher"`
	Model    string `json:"model"`
	Agent    string `json:"agent"`
	NoClaim  bool   `json:"no_claim"`
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

// filteredEnv returns os.Environ() with the named variables removed.
func filteredEnv(names ...string) []string {
	skip := make(map[string]bool, len(names))
	for _, n := range names {
		skip[n] = true
	}
	var env []string
	for _, e := range os.Environ() {
		key := e[:strings.IndexByte(e, '=')]
		if !skip[key] {
			env = append(env, e)
		}
	}
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
	Cooldown        time.Duration
	MaxTasks        int
	Review          bool
	MaxReviewRounds int
	LogFile         string
	Config          string
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
	fs.DurationVar(&cfg.Cooldown, "cooldown", 10*time.Second, "pause between tasks")
	fs.IntVar(&cfg.MaxTasks, "max-tasks", 0, "maximum tasks to process (0 = unlimited)")
	fs.BoolVar(&cfg.Review, "review", false, "enable PR review cycle (creates PR, reviews, fixes feedback, merges)")
	fs.IntVar(&cfg.MaxReviewRounds, "max-review-rounds", 3, "maximum review/fix iterations per PR")
	fs.StringVar(&cfg.LogFile, "log-file", "", "write structured logs to file (in addition to stderr)")
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

	repoName := filepath.Base(repoRoot)
	if cfg.Review {
		logger.Printf("loop: starting for %s (launcher=%s, review=on, max-rounds=%d, cooldown=%s)", repoName, cfg.Launcher, cfg.MaxReviewRounds, cfg.Cooldown)
	} else {
		logger.Printf("loop: starting for %s (launcher=%s, cooldown=%s)", repoName, cfg.Launcher, cfg.Cooldown)
	}

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	completed := 0
	failed := 0

	for iteration := 1; ; iteration++ {
		// Check for shutdown signal before starting next task.
		select {
		case sig := <-sigCh:
			logger.Printf("loop: received %s, stopping after %d completed, %d failed", sig, completed, failed)
			return nil
		default:
		}

		if cfg.MaxTasks > 0 && completed >= cfg.MaxTasks {
			logger.Printf("loop: reached max-tasks limit (%d), stopping", cfg.MaxTasks)
			break
		}

		logger.Printf("loop: iteration %d — checking for ready issues", iteration)

		ready, err := loadReadyIssues(repoRoot, cmd)
		if err != nil {
			logger.Printf("loop: error loading ready issues: %v", err)
			return err
		}
		if len(ready) == 0 {
			logger.Printf("loop: no ready issues remaining, stopping after %d completed, %d failed", completed, failed)
			break
		}

		selected := ready[0]
		logger.Printf("loop: selected %s — %s (priority=%d, type=%s)", selected.ID, selected.Title, selected.Priority, selected.IssueType)

		// Load full issue details.
		full, err := loadIssue(repoRoot, selected.ID, cmd)
		if err != nil {
			logger.Printf("loop: error loading issue %s: %v", selected.ID, err)
			return err
		}

		// Claim.
		if _, err := cmd.Run(repoRoot, "bd", "update", full.ID, "--claim", "--json"); err != nil {
			logger.Printf("loop: error claiming %s: %v", full.ID, err)
			return err
		}
		logger.Printf("loop: claimed %s", full.ID)

		// Build and launch.
		prompt := buildRP1Prompt(repoRoot, full, cfg.Review)
		nextCfg := config{
			Launcher: cfg.Launcher,
			Model:    cfg.Model,
			Agent:    cfg.Agent,
		}
		launchArgs, err := buildLaunchArgs(nextCfg, repoRoot, prompt)
		if err != nil {
			logger.Printf("loop: error building launch args for %s: %v", full.ID, err)
			return err
		}

		logger.Printf("loop: launching %s for %s", cfg.Launcher, full.ID)
		startTime := time.Now()
		launchErr := cmd.Start(repoRoot, stdin, stdout, stderr, cfg.Launcher, launchArgs...)
		elapsed := time.Since(startTime).Truncate(time.Second)

		if launchErr != nil {
			failed++
			logger.Printf("loop: %s build failed after %s — %v (completed=%d, failed=%d)", full.ID, elapsed, launchErr, completed, failed)
			goto cooldown
		}

		if cfg.Review {
			// Review cycle: detect PR → review → fix feedback → re-review → merge.
			branchName := buildRequirementName(full)
			prNumber, prErr := detectPR(repoRoot, branchName, cmd)
			if prErr != nil {
				failed++
				logger.Printf("loop: no PR found for %s (branch %s): %v", full.ID, branchName, prErr)
				goto cooldown
			}
			logger.Printf("loop: detected PR #%d for %s", prNumber, full.ID)

			approved := false
			for round := 1; round <= cfg.MaxReviewRounds; round++ {
				logger.Printf("loop: review round %d/%d for PR #%d (%s)", round, cfg.MaxReviewRounds, prNumber, full.ID)

				// Launch review agent.
				reviewPrompt := fmt.Sprintf("/pr-review %d", prNumber)
				if err := launchAgent(cfg, repoRoot, reviewPrompt, stdin, stdout, stderr, cmd); err != nil {
					logger.Printf("loop: review agent failed for PR #%d: %v", prNumber, err)
					break
				}

				verdict := parseReviewVerdict(repoRoot)
				logger.Printf("loop: review verdict for PR #%d: %s", prNumber, verdict)

				if verdict == verdictApprove {
					approved = true
					break
				}
				if verdict == verdictBlock {
					logger.Printf("loop: PR #%d blocked by review, skipping", prNumber)
					break
				}

				// request_changes or unknown — launch fix agent.
				if round >= cfg.MaxReviewRounds {
					logger.Printf("loop: exhausted %d review rounds for PR #%d", cfg.MaxReviewRounds, prNumber)
					break
				}

				logger.Printf("loop: launching fix agent for PR #%d (round %d)", prNumber, round)
				fixPrompt := fmt.Sprintf("/address-pr-feedback %d --afk", prNumber)
				if err := launchAgent(cfg, repoRoot, fixPrompt, stdin, stdout, stderr, cmd); err != nil {
					logger.Printf("loop: fix agent failed for PR #%d: %v", prNumber, err)
					break
				}

				// Push fixes that address-pr-feedback committed locally.
				if _, pushErr := cmd.Run(repoRoot, "git", "push", "origin", branchName); pushErr != nil {
					logger.Printf("loop: warning: git push failed for branch %s: %v", branchName, pushErr)
				}
			}

			if approved {
				if err := mergePR(repoRoot, prNumber, cmd); err != nil {
					failed++
					logger.Printf("loop: failed to merge PR #%d: %v", prNumber, err)
				} else {
					logger.Printf("loop: merged PR #%d", prNumber)
					closeReason := fmt.Sprintf("Completed by autopilot loop — PR #%d merged (launcher=%s, elapsed=%s)", prNumber, cfg.Launcher, time.Since(startTime).Truncate(time.Second))
					if _, err := cmd.Run(repoRoot, "bd", "close", full.ID, "--reason", closeReason, "--json"); err != nil {
						logger.Printf("loop: warning: failed to close %s: %v", full.ID, err)
					} else {
						logger.Printf("loop: closed %s", full.ID)
					}
					completed++
					logger.Printf("loop: %s completed in %s (completed=%d, failed=%d)", full.ID, time.Since(startTime).Truncate(time.Second), completed, failed)
				}
			} else {
				failed++
				logger.Printf("loop: %s not approved after review (completed=%d, failed=%d)", full.ID, completed, failed)
			}
		} else {
			// No review — close on agent success.
			reason := fmt.Sprintf("Completed by autopilot loop (launcher=%s, elapsed=%s)", cfg.Launcher, elapsed)
			if _, err := cmd.Run(repoRoot, "bd", "close", full.ID, "--reason", reason, "--json"); err != nil {
				logger.Printf("loop: warning: failed to close %s: %v", full.ID, err)
			} else {
				logger.Printf("loop: closed %s", full.ID)
			}
			completed++
			logger.Printf("loop: %s completed in %s (completed=%d, failed=%d)", full.ID, elapsed, completed, failed)
		}

	cooldown:
		if cfg.Cooldown > 0 {
			logger.Printf("loop: cooling down %s before next task", cfg.Cooldown)
			select {
			case sig := <-sigCh:
				logger.Printf("loop: received %s during cooldown, stopping after %d completed, %d failed", sig, completed, failed)
				return nil
			case <-time.After(cfg.Cooldown):
			}
		}
	}

	logger.Printf("loop: done — %d completed, %d failed", completed, failed)
	return nil
}

// launchAgent starts a short-lived agent session with the given prompt.
func launchAgent(cfg loopConfig, repoRoot string, prompt string, stdin io.Reader, stdout io.Writer, stderr io.Writer, cmd runner) error {
	agentCfg := config{
		Launcher: cfg.Launcher,
		Model:    cfg.Model,
		Agent:    cfg.Agent,
	}
	args, err := buildLaunchArgs(agentCfg, repoRoot, prompt)
	if err != nil {
		return err
	}
	return cmd.Start(repoRoot, stdin, stdout, stderr, cfg.Launcher, args...)
}

// detectPR finds an open PR for the given head branch using gh.
func detectPR(repoRoot string, branch string, cmd runner) (int, error) {
	output, err := cmd.Run(repoRoot, "gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number", "--limit", "1")
	if err != nil {
		return 0, fmt.Errorf("gh pr list: %w", err)
	}

	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(output, &prs); err != nil {
		return 0, fmt.Errorf("parse PR list: %w", err)
	}
	if len(prs) == 0 {
		return 0, fmt.Errorf("no open PR found for branch %s", branch)
	}
	return prs[0].Number, nil
}

// Review verdict constants.
const (
	verdictApprove        = "approve"
	verdictRequestChanges = "request_changes"
	verdictBlock          = "block"
	verdictUnknown        = "unknown"
)

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
		args := []string{"--model", cfg.Model, "--dangerously-skip-permissions", prompt}
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

	var issues []issue
	if err := json.Unmarshal(output, &issues); err != nil {
		return nil, fmt.Errorf("parse bd ready output: %w", err)
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
	if gitPR {
		return fmt.Sprintf(`/rp1-build %s %s --git-pr --afk`, quoteSlashArg(requirement), quoteSlashArg(description))
	}
	return fmt.Sprintf(`/rp1-build %s %s --afk`, quoteSlashArg(requirement), quoteSlashArg(description))
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

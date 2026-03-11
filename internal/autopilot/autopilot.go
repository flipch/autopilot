package autopilot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	return cmd.Run()
}

func (execRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	return run(args, stdin, stdout, stderr, execRunner{})
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, cmd runner) error {
	_ = stderr

	if len(args) == 0 {
		return errors.New("usage: autopilot <next|version> [flags]")
	}

	switch args[0] {
	case "next":
		cfg, err := parseNextArgs(args[1:])
		if err != nil {
			return err
		}
		return runNext(cfg, stdin, stdout, stderr, cmd)
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

	prompt := buildRP1Prompt(repoRoot, selected)
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
		args := []string{"--model", cfg.Model, prompt}
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

func buildRP1Prompt(repoRoot string, item issue) string {
	requirement := buildRequirementName(item)
	description := buildRequirementDescription(repoRoot, item)
	return fmt.Sprintf(`/rp1-build %s %s --git-pr --afk`, quoteSlashArg(requirement), quoteSlashArg(description))
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

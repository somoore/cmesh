package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrGitUnavailable = errors.New("git executable not found")

type Repository struct {
	Dir string
	Git string
}

type CommitOptions struct {
	AuthorName  string
	AuthorEmail string
	Subject     string
	Body        string
	TaskID      string
	OriginNode  string
	WorkerNode  string
	Profile     string
	AllowEmpty  bool
	// HandoffFrom names the task whose branch seeded this one, when this task
	// began as a cross-peer handoff.
	HandoffFrom string
}

type Status struct {
	IsRepo     bool
	Dirty      bool
	Porcelain  string
	Branch     string
	HeadCommit string
	Root       string
}

func Open(dir string) (*Repository, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, ErrGitUnavailable
	}
	cwd, err := CanonicalPath(dir)
	if err != nil {
		return nil, err
	}
	return &Repository{Dir: cwd, Git: git}, nil
}

func CanonicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty path")
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err == nil {
		abs = eval
	}
	return filepath.Clean(abs), nil
}

func (r *Repository) Status(ctx context.Context) (Status, error) {
	root, err := r.output(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return Status{IsRepo: false}, nil
	}
	branch, _ := r.output(ctx, "branch", "--show-current")
	head, _ := r.output(ctx, "rev-parse", "HEAD")
	porcelain, err := r.output(ctx, "status", "--porcelain")
	if err != nil {
		return Status{}, err
	}
	return Status{
		IsRepo:     true,
		Dirty:      strings.TrimSpace(porcelain) != "",
		Porcelain:  porcelain,
		Branch:     strings.TrimSpace(branch),
		HeadCommit: strings.TrimSpace(head),
		Root:       strings.TrimSpace(root),
	}, nil
}

func (r *Repository) RequireClean(ctx context.Context) error {
	st, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if !st.IsRepo {
		return errors.New("working directory is not a git repository")
	}
	if st.Dirty {
		return fmt.Errorf("dirty worktree:\n%s", st.Porcelain)
	}
	return nil
}

func DeterministicBranch(taskID, slug string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", errors.New("empty task id")
	}
	slug = branchSlug(slug)
	if slug == "" {
		slug = "task"
	}
	branch := "cmesh/" + taskID + "/" + slug
	if strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return "", fmt.Errorf("invalid branch name %q", branch)
	}
	return branch, nil
}

func (r *Repository) CheckoutBranch(ctx context.Context, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("empty branch")
	}
	return r.run(ctx, "checkout", "-B", branch)
}

func (r *Repository) CheckoutBranchAt(ctx context.Context, branch, base string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("empty branch")
	}
	if strings.TrimSpace(base) == "" {
		base = "HEAD"
	}
	return r.run(ctx, "checkout", "-B", branch, base)
}

func (r *Repository) AddWorktree(ctx context.Context, path, branch, base string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty worktree path")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("empty branch")
	}
	if strings.TrimSpace(base) == "" {
		base = "HEAD"
	}
	return r.run(ctx, "worktree", "add", "-B", branch, path, base)
}

func (r *Repository) RemoveWorktree(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty worktree path")
	}
	return r.run(ctx, "worktree", "remove", "--force", path)
}

func (r *Repository) PruneWorktrees(ctx context.Context) error {
	return r.run(ctx, "worktree", "prune")
}

func (r *Repository) AddAll(ctx context.Context) error {
	return r.run(ctx, "add", "-A")
}

func (r *Repository) Commit(ctx context.Context, opts CommitOptions) (string, error) {
	if strings.TrimSpace(opts.AuthorName) == "" {
		return "", errors.New("author name is required")
	}
	if strings.TrimSpace(opts.AuthorEmail) == "" {
		return "", errors.New("author email is required")
	}
	subject := strings.TrimSpace(opts.Subject)
	if subject == "" {
		subject = "cmesh task " + opts.TaskID
	}
	message := buildMessage(subject, opts)
	args := []string{
		"-c", "user.name=" + opts.AuthorName,
		"-c", "user.email=" + opts.AuthorEmail,
		"commit", "-m", message,
	}
	if opts.AllowEmpty {
		args = append(args, "--allow-empty")
	}
	if err := r.run(ctx, args...); err != nil {
		return "", err
	}
	return r.Head(ctx)
}

// VerifyBundle checks that this repository holds every prerequisite the bundle
// declares. cmesh bundles are thin -- Bundle appends ^<base> -- so a receiving
// checkout that lacks the base cannot apply one. Verify asks git which
// prerequisites the bundle actually needs rather than probing a single commit we
// assume is the only one.
func (r *Repository) VerifyBundle(ctx context.Context, bundlePath string) error {
	if strings.TrimSpace(bundlePath) == "" {
		return errors.New("empty bundle path")
	}
	// git exits non-zero for a missing prerequisite on the versions tested, and
	// the output check below is a belt-and-braces guard for versions that report
	// it only on stderr. Getting this wrong is expensive: a bundle that cannot
	// apply would pass verification and fail later as an opaque fetch error
	// instead of the actionable handoff_base_missing.
	combined, err := r.combinedOutput(ctx, "bundle", "verify", bundlePath)
	if err != nil {
		return err
	}
	if strings.Contains(combined, "lacks these prerequisite commits") {
		return fmt.Errorf("repository lacks prerequisite commits:\n%s", strings.TrimSpace(combined))
	}
	return nil
}

// combinedOutput runs git and returns stdout and stderr together. Some git
// subcommands report a meaningful diagnosis on stderr while still exiting 0.
func (r *Repository) combinedOutput(ctx context.Context, args ...string) (string, error) {
	if r == nil || r.Git == "" {
		return "", ErrGitUnavailable
	}
	cmd := exec.CommandContext(ctx, r.Git, args...)
	cmd.Dir = r.Dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return buf.String(), nil
}

// FetchBundle fetches a branch out of a bundle into this repository under the
// given local ref. The bundle is a plain file, so this adds no network access.
func (r *Repository) FetchBundle(ctx context.Context, bundlePath, branch, localRef string) error {
	if strings.TrimSpace(bundlePath) == "" {
		return errors.New("empty bundle path")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("empty branch")
	}
	if strings.TrimSpace(localRef) == "" {
		return errors.New("empty local ref")
	}
	return r.run(ctx, "fetch", bundlePath, branch+":"+localRef)
}

func (r *Repository) Head(ctx context.Context) (string, error) {
	out, err := r.output(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Repository) DiffStat(ctx context.Context, base, head string) (string, error) {
	return r.output(ctx, "diff", "--stat", revRange(base, head))
}

func (r *Repository) ChangedFiles(ctx context.Context, base, head string) ([]string, error) {
	out, err := r.output(ctx, "diff", "--name-only", revRange(base, head))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func (r *Repository) Bundle(ctx context.Context, path, branch, base string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty bundle path")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("empty branch")
	}
	args := []string{"bundle", "create", path, branch}
	if strings.TrimSpace(base) != "" {
		args = append(args, "^"+base)
	}
	return r.run(ctx, args...)
}

func (r *Repository) Push(ctx context.Context, remote, branch string) error {
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("empty branch")
	}
	return r.run(ctx, "push", remote, branch)
}

func (r *Repository) DeleteBranch(ctx context.Context, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("empty branch")
	}
	return r.run(ctx, "branch", "-D", branch)
}

func (r *Repository) run(ctx context.Context, args ...string) error {
	_, err := r.output(ctx, args...)
	return err
}

func (r *Repository) output(ctx context.Context, args ...string) (string, error) {
	if r == nil || r.Git == "" {
		return "", ErrGitUnavailable
	}
	cmd := exec.CommandContext(ctx, r.Git, args...)
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

func revRange(base, head string) string {
	base = strings.TrimSpace(base)
	head = strings.TrimSpace(head)
	if base == "" {
		base = "HEAD"
	}
	if head == "" {
		head = "HEAD"
	}
	return base + ".." + head
}

func buildMessage(subject string, opts CommitOptions) string {
	var b strings.Builder
	b.WriteString(subject)
	if strings.TrimSpace(opts.Body) != "" {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(opts.Body))
	}
	writeFooter := func(key, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		b.WriteString("\n")
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(val))
	}
	b.WriteString("\n")
	writeFooter("cmesh-task", opts.TaskID)
	writeFooter("cmesh-origin", opts.OriginNode)
	writeFooter("cmesh-worker", opts.WorkerNode)
	writeFooter("cmesh-profile", opts.Profile)
	// Handoff lineage lives in the commit object itself, so "where did this work
	// come from" is answerable from the git history alone, without cmesh's
	// database.
	writeFooter("cmesh-handoff-from", opts.HandoffFrom)
	return b.String()
}

var slugRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func branchSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-._")
	}
	return s
}

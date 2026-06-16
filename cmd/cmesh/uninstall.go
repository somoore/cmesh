package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"cmesh/internal/config"
	"cmesh/internal/mcp/setupinstall"
	svcpkg "cmesh/internal/service"
	"cmesh/internal/tui"
)

// cmdUninstall mirrors the installer (Part 6b). It removes cmesh MCP
// registrations from selected agents, then offers separately-consented optional
// cleanup of the OS service, config file, data dir, and self-installed binary.
//
// The whole flow is idempotent / safe to run twice, every state change is
// confirmed, and destructive steps are never bundled.
func cmdUninstall(args []string) error {
	_ = args

	// Resolve the data dir BEFORE removing anything: config.Load resolves
	// Node.DataDir, and we must capture it before the optional config delete.
	configPath := config.DefaultPath()
	var resolvedDataDir string
	if cfg, err := config.Load(configPath); err == nil {
		resolvedDataDir = cfg.Node.DataDir
	}

	fmt.Println("cmesh uninstaller")
	fmt.Println()

	// 1) Remove MCP registrations from agents that currently have cmesh wired.
	installed := setupinstall.DetectInstalledAgents()
	var removedAgents []string
	if len(installed) == 0 {
		fmt.Println("No agents currently have the cmesh MCP registered.")
	} else {
		selected, err := pickAgents("Remove the cmesh MCP from which agents?", installed)
		if err != nil {
			if errors.Is(err, tui.ErrCancelled) {
				fmt.Println("Cancelled. No changes made.")
				return nil
			}
			return err
		}
		if len(selected) > 0 {
			fmt.Println()
			fmt.Printf("This will remove the cmesh MCP from: %s\n", strings.Join(agentNames(selected), ", "))
			ok, err := confirmYesNo("Proceed?", true)
			if err != nil {
				return err
			}
			if ok {
				fmt.Println()
				for _, agent := range selected {
					result := setupinstall.Uninstall(agent)
					printCheck(agent.Name, result.OK, result.Detail)
					if result.OK {
						removedAgents = append(removedAgents, agent.Name)
					}
				}
			}
		}
	}

	// Track an honest final report of what was removed vs left.
	report := &cleanupReport{removedAgents: removedAgents}

	// 2) Optional cleanup, each independently confirmed (never bundled).
	fmt.Println()
	maybeRemoveService(report)
	maybeRemoveConfig(configPath, report)
	maybeRemoveDataDir(resolvedDataDir, configPath, report)
	maybeRemoveBinary(report)

	report.print()
	return nil
}

type cleanupReport struct {
	removedAgents []string
	removed       []string
	scheduled     []string
	left          []string
}

func (r *cleanupReport) print() {
	fmt.Println()
	fmt.Println("Uninstall summary:")
	if len(r.removedAgents) > 0 {
		fmt.Printf("  Removed cmesh MCP from: %s\n", strings.Join(r.removedAgents, ", "))
	}
	for _, item := range r.removed {
		fmt.Printf("  Removed: %s\n", item)
	}
	for _, item := range r.scheduled {
		fmt.Printf("  Scheduled: %s\n", item)
	}
	for _, item := range r.left {
		fmt.Printf("  Left:    %s\n", item)
	}
	if len(r.removedAgents) == 0 && len(r.removed) == 0 && len(r.scheduled) == 0 && len(r.left) == 0 {
		fmt.Println("  Nothing to do.")
	}
}

func maybeRemoveService(report *cleanupReport) {
	ok, err := confirmYesNo("Stop and remove the cmesh OS service?", false)
	if err != nil || !ok {
		report.left = append(report.left, "OS service")
		return
	}
	// svcpkg.Remove() STOPS the daemon as part of removal (launchctl bootout /
	// systemctl stop / sc.exe stop) before deleting the unit, so a
	// service-managed daemon is torn down here. We deliberately do NOT hunt for
	// and kill a foreground `cmesh up` started by hand: matching processes by
	// name risks killing an unrelated PID, which an uninstaller must never do.
	// A hand-started daemon is the user's own process to stop.
	if err := removeServiceForUninstall(); err != nil {
		printCheck("service remove", false, err.Error())
		report.left = append(report.left, "OS service ("+err.Error()+")")
		return
	}
	printCheck("service", true, "stopped and removed")
	report.removed = append(report.removed, "OS service")
}

func removeServiceForUninstall() error {
	err := svcpkg.Remove()
	if err == nil {
		return nil
	}
	if runtime.GOOS != "windows" || !svcpkg.IsWindowsElevationRequired(err) {
		return err
	}
	fmt.Println("Windows will ask for permission to remove the cmesh service.")
	return runElevated("_remove-service")
}

func maybeRemoveConfig(configPath string, report *cleanupReport) {
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		// Nothing to remove; don't even ask.
		return
	}
	ok, err := confirmYesNo(fmt.Sprintf("Remove the cmesh config file (%s)?", configPath), false)
	if err != nil || !ok {
		report.left = append(report.left, "config file "+configPath)
		return
	}
	if err := os.Remove(configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		printCheck("config remove", false, err.Error())
		report.left = append(report.left, "config file ("+err.Error()+")")
		return
	}
	printCheck("config file", true, "removed "+configPath)
	report.removed = append(report.removed, "config file "+configPath)
}

func maybeRemoveDataDir(resolvedDataDir, configPath string, report *cleanupReport) {
	if strings.TrimSpace(resolvedDataDir) == "" {
		// We could not resolve a data dir (no config); nothing safe to remove.
		return
	}
	if _, err := os.Stat(resolvedDataDir); errors.Is(err, os.ErrNotExist) {
		return
	}

	home, _ := os.UserHomeDir()
	configRoot := configRootDir()

	// Path guard: refuse filesystem root, $HOME, the user config root, and any
	// ancestor of those. These protect against deleting something catastrophic.
	if guardErr := dataDirDeletable(resolvedDataDir, home, configRoot); guardErr != nil {
		printCheck("data dir", false, "refused to delete "+resolvedDataDir+": "+guardErr.Error())
		report.left = append(report.left, "data dir "+resolvedDataDir+" (guarded: "+guardErr.Error()+")")
		return
	}

	// Stronger, independent signal that this dir is really cmesh's: it must
	// directly contain a cmesh-created artifact (the task DB or audit log).
	// Without this, a config pointing data_dir at /etc, /opt, /var/lib, etc.
	// would pass every path check (none is $HOME/configRoot/root/ancestor) and
	// os.RemoveAll would proceed. We never delete a directory we cannot prove
	// cmesh populated.
	if !dataDirHasCmeshArtifacts(resolvedDataDir) {
		printCheck("data dir", false, "refused to delete "+resolvedDataDir+": no cmesh artifacts found (expected cmesh.db or audit.jsonl)")
		report.left = append(report.left, "data dir "+resolvedDataDir+" (no cmesh artifacts; left untouched)")
		return
	}

	fmt.Println()
	fmt.Println("WARNING: removing the data dir deletes your cmesh audit history,")
	fmt.Printf("         task database, logs, and artifacts under:\n         %s\n", resolvedDataDir)
	ok, err := confirmYesNo("Remove the cmesh data dir?", false)
	if err != nil || !ok {
		report.left = append(report.left, "data dir "+resolvedDataDir)
		return
	}
	if err := os.RemoveAll(resolvedDataDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		printCheck("data dir remove", false, err.Error())
		report.left = append(report.left, "data dir ("+err.Error()+")")
		return
	}
	printCheck("data dir", true, "removed "+resolvedDataDir)
	report.removed = append(report.removed, "data dir "+resolvedDataDir)
}

func maybeRemoveBinary(report *cleanupReport) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	// Only offer removal if the binary lives in a known self-install dir; we
	// never delete a binary cmesh did not place.
	if !selfInstalledBinary(exe) {
		fmt.Println()
		fmt.Printf("The cmesh binary (%s) was not installed via self-install; remove it manually if desired.\n", exe)
		report.left = append(report.left, "binary "+exe+" (remove manually)")
		return
	}

	fmt.Println()
	ok, err := confirmYesNo(fmt.Sprintf("Remove the cmesh binary (%s)?", exe), false)
	if err != nil || !ok {
		report.left = append(report.left, "binary "+exe)
		return
	}
	detail, err := removeCurrentExecutable(exe)
	if err != nil {
		printCheck("binary remove", false, err.Error())
		report.left = append(report.left, "binary ("+err.Error()+")")
		return
	}
	printCheck("binary", true, detail)
	if binaryRemovalScheduled(detail) {
		report.scheduled = append(report.scheduled, "binary "+exe)
	} else {
		report.removed = append(report.removed, "binary "+exe)
	}
}

func binaryRemovalScheduled(detail string) bool {
	return strings.HasPrefix(detail, "scheduled ")
}

// selfInstalledBinary reports whether exe lives in a directory self-install
// would have used (the only place we are willing to delete from).
func selfInstalledBinary(exe string) bool {
	dir := filepath.Clean(filepath.Dir(exe))
	candidates := []string{"/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin"))
	}
	if custom := strings.TrimSpace(os.Getenv("CMESH_INSTALL_DIR")); custom != "" {
		candidates = append(candidates, filepath.Clean(custom))
	}
	for _, c := range candidates {
		if filepath.Clean(c) == dir {
			return true
		}
	}
	return false
}

// dataDirDeletable is the path-safety guard for the data-dir delete. It refuses
// to delete paths that are catastrophic to remove (filesystem root, $HOME, the
// user config root, an empty path, or any ancestor of $HOME/config root). It is
// pure so it can be unit-tested directly.
//
// It deliberately does NOT try to prove the path "belongs" to cmesh via a path
// comparison — an earlier version compared the resolved path against a value
// that was always the path itself, so the check was a tautology that always
// passed. Proof that a directory is really cmesh's is established separately and
// far more strongly by dataDirHasCmeshArtifacts.
func dataDirDeletable(resolved, home, configRoot string) error {
	if strings.TrimSpace(resolved) == "" {
		return errors.New("empty path")
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return err
	}
	clean := filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		clean = filepath.Clean(real)
	}

	if clean == string(filepath.Separator) || clean == "." {
		return errors.New("refuses to delete filesystem root")
	}
	for _, forbidden := range []string{home, configRoot} {
		if forbidden == "" {
			continue
		}
		fclean := filepath.Clean(forbidden)
		if real, err := filepath.EvalSymlinks(fclean); err == nil {
			fclean = filepath.Clean(real)
		}
		if clean == fclean {
			return fmt.Errorf("refuses to delete %s", fclean)
		}
		// Never delete an ancestor of a protected directory (e.g. $HOME's parent).
		if isAncestor(clean, fclean) {
			return fmt.Errorf("path is an ancestor of %s", fclean)
		}
	}
	return nil
}

// cmeshArtifactNames are the files cmesh creates directly inside its data dir by
// default (see internal/config: Store.Path = <data_dir>/cmesh.db,
// Audit.Path = <data_dir>/audit.jsonl). The presence of any one of these is a
// strong, independent signal that the directory really is a cmesh data dir.
var cmeshArtifactNames = []string{"cmesh.db", "audit.jsonl"}

// dataDirHasCmeshArtifacts reports whether dir directly contains at least one
// recognizable cmesh-created artifact. This is the primary safeguard against
// deleting a directory that merely happens to be named/configured as the data
// dir but was never populated by cmesh (e.g. data_dir mistakenly set to /etc).
func dataDirHasCmeshArtifacts(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	for _, name := range cmeshArtifactNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// isAncestor reports whether ancestor is a strict parent directory of child.
func isAncestor(ancestor, child string) bool {
	ancestor = filepath.Clean(ancestor)
	child = filepath.Clean(child)
	if ancestor == child {
		return false
	}
	rel, err := filepath.Rel(ancestor, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

func configRootDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		if home, herr := os.UserHomeDir(); herr == nil {
			return filepath.Join(home, ".config")
		}
		return ""
	}
	return dir
}

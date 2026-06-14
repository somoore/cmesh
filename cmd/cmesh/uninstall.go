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
	for _, item := range r.left {
		fmt.Printf("  Left:    %s\n", item)
	}
	if len(r.removedAgents) == 0 && len(r.removed) == 0 && len(r.left) == 0 {
		fmt.Println("  Nothing to do.")
	}
}

func maybeRemoveService(report *cleanupReport) {
	ok, err := confirmYesNo("Stop and remove the cmesh OS service?", false)
	if err != nil || !ok {
		report.left = append(report.left, "OS service")
		return
	}
	if err := svcpkg.Remove(); err != nil {
		printCheck("service remove", false, err.Error())
		report.left = append(report.left, "OS service ("+err.Error()+")")
		return
	}
	printCheck("service", true, "stopped and removed")
	report.removed = append(report.removed, "OS service")
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

	// Bound against what config actually says the data dir is (not a fixed
	// default, which would refuse legitimate custom data dirs). This clause
	// mainly catches symlink escapes; the home/configRoot/root checks are what
	// protect $HOME.
	if guardErr := dataDirDeletable(resolvedDataDir, home, configRoot, resolvedDataDir); guardErr != nil {
		printCheck("data dir", false, "refused to delete "+resolvedDataDir+": "+guardErr.Error())
		report.left = append(report.left, "data dir "+resolvedDataDir+" (guarded: "+guardErr.Error()+")")
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
	// never delete a binary cmesh did not place. Deleting the running binary is
	// also unreliable on Windows, so always print a manual hint there.
	if runtime.GOOS == "windows" || !selfInstalledBinary(exe) {
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
	if err := os.Remove(exe); err != nil && !errors.Is(err, os.ErrNotExist) {
		printCheck("binary remove", false, err.Error())
		report.left = append(report.left, "binary ("+err.Error()+")")
		return
	}
	printCheck("binary", true, "removed "+exe)
	report.removed = append(report.removed, "binary "+exe)
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

// dataDirDeletable is the security guard for the data-dir delete. It refuses to
// delete paths that are dangerous to remove (root, $HOME, the user config root,
// empty) and requires the resolved path to actually live at or under the
// configured cmesh data dir. It is pure so it can be unit-tested directly.
func dataDirDeletable(resolved, home, configRoot, configuredDataDir string) error {
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

	// Must live at or under the configured cmesh data dir.
	if configuredDataDir != "" {
		want := filepath.Clean(configuredDataDir)
		if real, err := filepath.EvalSymlinks(want); err == nil {
			want = filepath.Clean(real)
		}
		if clean != want && !isAncestor(want, clean) {
			return fmt.Errorf("not under the configured cmesh data dir %s", want)
		}
	}
	return nil
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

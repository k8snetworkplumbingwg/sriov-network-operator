package v1

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/consts"
	"github.com/k8snetworkplumbingwg/sriov-network-operator/pkg/vars"
)

// logHostPathFolderNameRE: single folder name under /var/log (no separators).
var logHostPathFolderNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Both returns the effective log config for specific component.
// Applies global settings first, then component overrides.
func GetEffectiveConfigDaemonLogConfig(lc *LogConfig) (vars.LogFileSettings, error) {
	cfg, err := getEffectiveLogConfigBase(lc)
	if err != nil {
		return vars.LogFileSettings{}, err
	}
	if lc == nil || lc.ConfigDaemon == nil {
		return cfg, nil
	}
	return applyComponentOverride(cfg, lc.ConfigDaemon)
}

func GetEffectiveDevicePluginLogConfig(lc *LogConfig) (vars.LogFileSettings, error) {
	cfg, err := getEffectiveLogConfigBase(lc)
	if err != nil {
		return vars.LogFileSettings{}, err
	}
	if lc == nil || lc.DevicePlugin == nil {
		return cfg, nil
	}
	return applyComponentOverride(cfg, lc.DevicePlugin)
}

// getEffectiveLogConfigBase applies global LogConfig settings to defaults.
func getEffectiveLogConfigBase(lc *LogConfig) (vars.LogFileSettings, error) {
	cfg := vars.DefaultLogCfg()
	if lc == nil {
		return cfg, nil
	}
	if lc.Enabled != nil {
		cfg.Enabled = *lc.Enabled
	}
	if lc.MaxSizeMB != nil {
		cfg.MaxSizeMB = *lc.MaxSizeMB
	}
	if lc.MaxFiles != nil {
		cfg.MaxFiles = *lc.MaxFiles
	}
	if lc.MaxAgeDays != nil {
		cfg.MaxAgeDays = *lc.MaxAgeDays
	}
	if lc.HostPath != nil && *lc.HostPath != "" {
		resolved, err := ResolveLogHostPath(*lc.HostPath)
		if err != nil {
			return vars.LogFileSettings{}, err
		}
		cfg.HostPath = resolved
	}
	return cfg, nil
}

// applyComponentOverride applies ComponentLogConfig overrides to base config.
func applyComponentOverride(cfg vars.LogFileSettings, override *ComponentLogConfig) (vars.LogFileSettings, error) {
	if override == nil {
		return cfg, nil
	}
	if override.Enabled != nil {
		cfg.Enabled = *override.Enabled
	}
	if override.MaxSizeMB != nil {
		cfg.MaxSizeMB = *override.MaxSizeMB
	}
	if override.MaxFiles != nil {
		cfg.MaxFiles = *override.MaxFiles
	}
	if override.MaxAgeDays != nil {
		cfg.MaxAgeDays = *override.MaxAgeDays
	}
	if override.HostPath != nil && *override.HostPath != "" {
		resolved, err := ResolveLogHostPath(*override.HostPath)
		if err != nil {
			return vars.LogFileSettings{}, err
		}
		cfg.HostPath = resolved
	}
	return cfg, nil
}

// ResolveLogHostPath validates HostPath and returns a cleaned absolute path.
// Accepts: folder name → /var/log/<name>, or absolute path under /var/log.
// Rejects: empty, "..", "~", paths outside /var/log, and symlink escapes
func ResolveLogHostPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("hostPath must not be empty")
	}
	if strings.Contains(p, "~") {
		return "", fmt.Errorf("hostPath must not contain '~', got %q", p)
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("hostPath must not contain path traversal (..), got %q", p)
	}

	var resolved string
	if filepath.IsAbs(p) {
		resolved = filepath.Clean(p)
	} else {
		// relative → /var/log/<name>
		if strings.ContainsAny(p, `/\`) {
			return "", fmt.Errorf("hostPath folder name must not contain path separators, got %q; use a name like \"custom-sriov-network-operator\" or a full path under %s", p, consts.LogHostPathRoot)
		}
		if !logHostPathFolderNameRE.MatchString(p) {
			return "", fmt.Errorf("hostPath folder name %q is invalid; must start with an alphanumeric character and contain only [a-zA-Z0-9._-]", p)
		}
		resolved = filepath.Join(consts.LogHostPathRoot, p)
	}

	if err := checkLogHostPathAllowed(resolved); err != nil {
		return "", fmt.Errorf("hostPath %w, got %q", err, p)
	}

	// Resolve the nearest existing ancestor (path may not exist yet; MkdirAll creates it).
	// Reject unexpected Lstat errors and symlink escapes via ancestors under /var/log.
	resolved, err := resolveLogHostPathWithAncestors(p, resolved)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// resolveLogHostPathWithAncestors walks up from resolved to /var/log, finds the
// nearest existing ancestor, EvalSymlinks it, enforces /var/log containment, then
// re-appends any missing trailing components. If nothing under /var/log exists yet,
// returns the syntactically validated cleaned path (webhook-safe).
func resolveLogHostPathWithAncestors(original, resolved string) (string, error) {
	cur := resolved
	var missing []string

	for {
		_, err := os.Lstat(cur)
		if err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", fmt.Errorf("hostPath cannot resolve %q: %w", cur, err)
			}
			real = filepath.Clean(real)
			if err := checkLogHostPathAllowed(real); err != nil {
				return "", fmt.Errorf("hostPath %q resolves via %q to %q which %w", original, cur, real, err)
			}
			if len(missing) == 0 {
				return real, nil
			}
			for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
				missing[i], missing[j] = missing[j], missing[i]
			}
			out := filepath.Join(append([]string{real}, missing...)...)
			if err := checkLogHostPathAllowed(out); err != nil {
				return "", fmt.Errorf("hostPath %q resolves to %q which %w", original, out, err)
			}
			return out, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("hostPath cannot inspect %q: %w", cur, err)
		}
		// Do not walk above /var/log: a missing /var/log is fine (MkdirAll creates it).
		if cur == consts.LogHostPathRoot || !strings.HasPrefix(cur, consts.LogHostPathRoot+string(os.PathSeparator)) {
			return resolved, nil
		}
		missing = append(missing, filepath.Base(cur))
		parent := filepath.Dir(cur)
		if parent == cur {
			return resolved, nil
		}
		cur = parent
	}
}

// checkLogHostPathAllowed: path must be /var/log or under it.
func checkLogHostPathAllowed(path string) error {
	if path == "/" {
		return fmt.Errorf("must not be the filesystem root")
	}
	if path == consts.LogHostPathRoot {
		return nil
	}
	if !strings.HasPrefix(path+string(os.PathSeparator), consts.LogHostPathRoot+string(os.PathSeparator)) {
		return fmt.Errorf("must be under %s", consts.LogHostPathRoot)
	}
	return nil
}

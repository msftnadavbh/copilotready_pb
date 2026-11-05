package sessions

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var githubRemote = regexp.MustCompile(`^(?:https?://(?:[^@/]+@)?|ssh://git@|git@)github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

func githubRepository(remote string) (owner, name string, ok bool) {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// gitInfo reads the origin remote and current branch of the repository containing dir, without
// running Git. Worktrees keep their config in the common directory.
func gitInfo(dir string) (origin, branch string) {
	if dir == "" {
		return "", ""
	}
	for cur := filepath.Clean(dir); ; cur = filepath.Dir(cur) {
		dot := filepath.Join(cur, ".git")
		if info, err := os.Stat(dot); err == nil {
			gitDir := dot
			if !info.IsDir() {
				b, err := os.ReadFile(dot)
				v, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
				if err != nil || !ok {
					return "", ""
				}
				gitDir = absoluteFrom(cur, strings.TrimSpace(v))
			}
			common := gitDir
			if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common = absoluteFrom(gitDir, strings.TrimSpace(string(b)))
			}
			if b, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
				branch, _ = strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
				if branch == strings.TrimSpace(string(b)) {
					branch = "" // detached HEAD
				}
			}
			return originURL(filepath.Join(common, "config")), branch
		}
		if filepath.Dir(cur) == cur {
			return "", ""
		}
	}
}

func absoluteFrom(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

func originURL(config string) string {
	f, err := os.Open(config)
	if err != nil {
		return ""
	}
	defer f.Close()
	inOrigin := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if key, value, ok := strings.Cut(line, "="); inOrigin && ok && strings.TrimSpace(key) == "url" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

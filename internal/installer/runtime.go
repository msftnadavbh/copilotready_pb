package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const CLIVersion = "1.0.105"

type Asset struct{ Version, Platform, URL, SHA256, Archive, Executable string }
type runtimeState struct {
	Current  Runtime  `json:"current"`
	Previous *Runtime `json:"previous,omitempty"`
}

func PlatformAsset() (Asset, error) {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	key := platform + "-" + arch
	digests := map[string]string{
		"win32-x64":    "34e16e9cf47de0e1534920171d6f39fa246638bddbb6ece820870b382bcc061a",
		"win32-arm64":  "647876bca9ca6ea77f5a28d002e1d135d1bb899c12bcd3f2eb1e860d6199ed90",
		"darwin-arm64": "34aacd03fb478d47e4b3cfc5ca045c37278c5d8d86239466521a16ea54ac84aa",
		"darwin-x64":   "de5684eeb118ad1e54f9a01f4535cf74e121b8226d26473550b01a7565ce54f5",
	}
	digest, ok := digests[key]
	if !ok {
		return Asset{}, errors.New("this release supports Windows and macOS on x64/ARM64 only; Linux/WSL are unsupported")
	}
	archive, exe := "tar.gz", "copilot"
	if platform == "win32" {
		archive, exe = "zip", "copilot.exe"
	}
	return Asset{CLIVersion, key, "https://github.com/github/copilot-cli/releases/download/v" + CLIVersion + "/copilot-" + key + "." + archive, digest, archive, exe}, nil
}
func (m *Manager) readRuntimeState() (runtimeState, error) {
	var state runtimeState
	p := filepath.Join(m.Root, "runtime-state.json")
	if err := safePath(p, []string{m.Root}); err != nil {
		return state, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	return state, err
}
func (m *Manager) verify(rt Runtime) error {
	if err := safePath(rt.Path, []string{filepath.Join(m.Root, "runtimes")}); err != nil {
		return err
	}
	actual, err := Snapshot(rt.Path)
	if err != nil {
		return err
	}
	if actual == "absent" || actual != rt.SHA256 {
		return errors.New("managed Copilot executable changed or is missing; choose Repair rather than trusting the changed file")
	}
	return nil
}
func (m *Manager) VerifyRuntime() (Runtime, error) {
	state, err := m.readRuntimeState()
	if err != nil {
		return Runtime{}, err
	}
	return state.Current, m.verify(state.Current)
}
func (m *Manager) EnsureRuntime(ctx context.Context) (Runtime, error) {
	asset, err := PlatformAsset()
	if err != nil {
		return Runtime{}, err
	}
	if rt, err := m.VerifyRuntime(); err == nil {
		if rt.Platform != asset.Platform {
			return Runtime{}, errors.New("managed runtime belongs to a different platform; choose Repair explicitly")
		}
		order, known := compareVersions(rt.Version, asset.Version)
		if !known || order > 0 {
			return Runtime{}, errors.New("managed runtime is newer or its version is unrecognized; use the latest setup instead of downgrading")
		}
		if order == 0 {
			return rt, nil
		}
		return m.installAsset(ctx, asset, downloadClient())
	}
	if _, err := os.Lstat(filepath.Join(m.Root, "runtime-state.json")); !os.IsNotExist(err) {
		return Runtime{}, errors.New("managed runtime needs Repair")
	}
	return m.installAsset(ctx, asset, downloadClient())
}

// Unknown versions require review rather than an accidental downgrade.
func compareVersions(a, b string) (int, bool) {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	if len(left) != 3 || len(right) != 3 {
		return 0, false
	}
	order := 0
	for i := range left {
		x, ex := strconv.ParseUint(left[i], 10, 64)
		y, ey := strconv.ParseUint(right[i], 10, 64)
		if ex != nil || ey != nil {
			return 0, false
		}
		if order == 0 && x != y {
			order = -1
			if x > y {
				order = 1
			}
		}
	}
	return order, true
}
func (m *Manager) RepairRuntime(ctx context.Context) (Runtime, error) {
	asset, err := PlatformAsset()
	if err != nil {
		return Runtime{}, err
	}
	return m.installAsset(ctx, asset, downloadClient())
}
func downloadClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("download redirect must use HTTPS")
		}
		if len(via) > 10 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}

func (m *Manager) installAsset(ctx context.Context, asset Asset, client *http.Client) (Runtime, error) {
	release, err := m.Acquire()
	if err != nil {
		return Runtime{}, err
	}
	defer release()
	// Recheck under the lock: another setup may have upgraded after EnsureRuntime's check.
	if current, e := m.VerifyRuntime(); e == nil {
		if order, known := compareVersions(current.Version, asset.Version); !known || order > 0 {
			return Runtime{}, errors.New("managed runtime is newer or its version is unrecognized; use the latest setup instead of downgrading")
		}
	}
	runtimes := filepath.Join(m.Root, "runtimes")
	if err = safePath(runtimes, []string{m.Root}); err != nil {
		return Runtime{}, err
	}
	if err = os.MkdirAll(runtimes, 0700); err != nil {
		return Runtime{}, err
	}
	stage, err := os.MkdirTemp(runtimes, ".stage-")
	if err != nil {
		return Runtime{}, err
	}
	defer os.RemoveAll(stage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return Runtime{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Runtime{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Runtime{}, fmt.Errorf("official download returned HTTP %d", resp.StatusCode)
	}
	archive := filepath.Join(stage, "download")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Runtime{}, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 512*1024*1024+1))
	closeErr := f.Close()
	if copyErr != nil {
		return Runtime{}, copyErr
	}
	if closeErr != nil {
		return Runtime{}, closeErr
	}
	if n > 512*1024*1024 {
		return Runtime{}, errors.New("download exceeds release size limit")
	}
	if hex.EncodeToString(h.Sum(nil)) != asset.SHA256 {
		return Runtime{}, errors.New("official download digest mismatch; nothing was executed")
	}
	extracted := filepath.Join(stage, asset.Executable)
	if err = extractExecutable(archive, asset, extracted); err != nil {
		return Runtime{}, err
	}
	binaryHash, err := Snapshot(extracted)
	if err != nil {
		return Runtime{}, err
	}
	if err = os.Remove(archive); err != nil {
		return Runtime{}, err
	}
	final := filepath.Join(runtimes, asset.Version+"-"+asset.Platform+"-"+id())
	rt := Runtime{asset.Version, filepath.Join(final, asset.Executable), binaryHash, asset.Platform}
	if err = writeJSON(filepath.Join(stage, "runtime.json"), rt); err != nil {
		return Runtime{}, err
	}
	if err = os.Rename(stage, final); err != nil {
		return Runtime{}, err
	}
	state := runtimeState{Current: rt}
	if previous, e := m.readRuntimeState(); e == nil {
		if m.verify(previous.Current) == nil {
			state.Previous = &previous.Current
		} else if previous.Previous != nil && m.verify(*previous.Previous) == nil {
			state.Previous = previous.Previous
		}
	}
	if err = writeJSON(filepath.Join(m.Root, "runtime-state.json"), state); err != nil {
		return Runtime{}, err
	}
	return rt, nil
}
func validArchiveName(name string) bool {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func extractExecutable(archive string, asset Asset, dest string) error {
	found := false
	write := func(r io.Reader, size int64) error {
		if found {
			return errors.New("duplicate executable in archive")
		}
		if size < 1 || size > 512*1024*1024 {
			return errors.New("unexpected executable size")
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			return err
		}
		n, e := io.CopyN(f, r, size)
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if n != size {
			return errors.New("truncated executable")
		}
		found = true
		return nil
	}
	if asset.Archive == "zip" {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if !validArchiveName(f.Name) || f.Mode()&os.ModeSymlink != 0 {
				return errors.New("unsafe archive entry")
			}
			if filepath.ToSlash(f.Name) == asset.Executable {
				r, e := f.Open()
				if e != nil {
					return e
				}
				e = write(r, int64(f.UncompressedSize64))
				r.Close()
				if e != nil {
					return e
				}
			}
		}
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			header, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			if !validArchiveName(header.Name) || header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
				return errors.New("unsafe archive entry")
			}
			name := strings.TrimPrefix(header.Name, "./")
			if name == asset.Executable {
				if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
					return errors.New("executable is not a regular file")
				}
				if e = write(tr, header.Size); e != nil {
					return e
				}
			}
		}
	}
	if !found {
		return errors.New("official archive did not contain the expected executable")
	}
	return nil
}
func (m *Manager) Rollback() error {
	release, err := m.Acquire()
	if err != nil {
		return err
	}
	defer release()
	state, err := m.readRuntimeState()
	if err != nil {
		return err
	}
	if state.Previous == nil {
		return errors.New("no previous verified runtime is available")
	}
	if err = m.verify(*state.Previous); err != nil {
		return err
	}
	previous := state.Current
	state.Current = *state.Previous
	state.Previous = &previous
	return writeJSON(filepath.Join(m.Root, "runtime-state.json"), state)
}

// Environment isolates the managed profile and package cache for every native caller.
func (m *Manager) Environment(profile string) []string {
	drop := map[string]bool{"COPILOT_HOME": true, "COPILOT_CACHE_HOME": true, "COPILOT_PKG_CACHE_HOME": true, "COPILOT_CLI_DIST_DIR": true, "COPILOT_ALLOW_ALL": true, "COPILOT_ASSISTED_APPROVAL": true, "COPILOT_MODEL": true}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[strings.ToUpper(key)] {
			env = append(env, entry)
		}
	}
	cache := filepath.Join(m.Root, "cache")
	return append(env, "COPILOT_HOME="+profile, "COPILOT_CACHE_HOME="+cache, "COPILOT_PKG_CACHE_HOME="+cache)
}

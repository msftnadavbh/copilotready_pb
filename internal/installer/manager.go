package installer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"copilotready/internal/core"
)

const owner = "copilot-ready-v2\n"

type Manager struct{ Root, Home string }
type Entry struct {
	Path         string `json:"path"`
	Backup       string `json:"backup"`
	BeforeSHA256 string `json:"beforeSHA256"`
	AfterSHA256  string `json:"afterSHA256"`
	Applied      bool   `json:"applied"`
	BeforeMode   uint32 `json:"beforeMode,omitempty"`
}
type Transaction struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	Entries      []Entry  `json:"entries"`
	AllowedRoots []string `json:"allowedRoots"`
}
type Runtime struct {
	Version  string `json:"version"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Platform string `json:"platform"`
}
type LaunchOptions struct {
	Profile string `json:"profile"`
	Project string `json:"project"`
	Sandbox string `json:"sandbox"`
	GitAuth bool   `json:"gitAuth"`
	// Normal is the user's everyday profile; Open Copilot then runs the copilot command itself.
	Normal bool `json:"normal,omitempty"`
}
type StatusInfo struct {
	Root         string         `json:"root"`
	Installed    bool           `json:"installed"`
	Runtime      *Runtime       `json:"runtime,omitempty"`
	Launch       *LaunchOptions `json:"launch,omitempty"`
	Transactions []string       `json:"transactions"`
	Warnings     []string       `json:"warnings"`
	// Setup is the finished setup transaction that Undo restores, with everything after it.
	Setup string `json:"setup,omitempty"`
}

func New(root, home string) *Manager {
	return &Manager{Root: filepath.Clean(root), Home: filepath.Clean(home)}
}
// id sorts by creation time, to the nanosecond, so Undo can order transactions.
func id() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return time.Now().UTC().Format("20060102T150405.000000000") + "-" + hex.EncodeToString(b)
}
func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func Snapshot(path string) (string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// safePath rejects symlinks/junctions, traversal, and unexpected roots before writes.
func safePath(path string, roots []string) error {
	if !filepath.IsAbs(path) {
		return errors.New("write path must be absolute")
	}
	path = filepath.Clean(path)
	contained := false
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(root), path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			contained = true
			break
		}
	}
	if !contained {
		return fmt.Errorf("path is outside reviewed destinations: %s", path)
	}
	for cursor := path; ; cursor = filepath.Dir(cursor) {
		info, err := os.Lstat(cursor)
		if err == nil && linked(info) {
			return fmt.Errorf("linked paths need manual handling: %s", cursor)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
	}
	return nil
}
func (m *Manager) EnsureRoot() error {
	if !filepath.IsAbs(m.Root) || m.Root == m.Home || filepath.Dir(m.Root) == m.Root {
		return errors.New("unsafe managed root")
	}
	if err := safePath(m.Root, []string{m.Root}); err != nil {
		return err
	}
	if info, err := os.Stat(m.Root); err == nil {
		if !info.IsDir() {
			return errors.New("managed root is not a folder")
		}
		marker := filepath.Join(m.Root, ".owner")
		if err := safePath(marker, []string{m.Root}); err != nil {
			return err
		}
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != owner {
			return errors.New("destination exists and is not owned by Copilot Ready")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(m.Root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".copilot-ready-create-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err = os.WriteFile(filepath.Join(staging, ".owner"), []byte(owner), 0600); err != nil {
		return err
	}
	if err = os.Rename(staging, m.Root); err != nil {
		if b, e := os.ReadFile(filepath.Join(m.Root, ".owner")); e == nil && string(b) == owner {
			return nil
		}
		return err
	}
	return nil
}
func (m *Manager) Acquire() (func(), error) {
	if err := m.EnsureRoot(); err != nil {
		return nil, err
	}
	path := filepath.Join(m.Root, ".setup.lock")
	if err := safePath(path, []string{m.Root}); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, errors.New("another setup operation is still running")
	}
	f.Truncate(0)
	f.WriteString(fmt.Sprintf("pid=%d\nstarted=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)))
	return func() { unlockFile(f); f.Close() }, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0600
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(parent, ".copilot-ready-write-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceFile(temp, path)
}
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}
func (m *Manager) journal(tx Transaction) error {
	return writeJSON(filepath.Join(m.Root, "transactions", tx.ID, "journal.json"), tx)
}

func (m *Manager) Apply(changes []core.Change, allowedRoots []string) (Transaction, error) {
	tx := Transaction{ID: id(), Status: "preparing", AllowedRoots: allowedRoots}
	release, err := m.Acquire()
	if err != nil {
		return tx, err
	}
	defer release()
	seen := map[string]bool{}
	for _, change := range changes {
		if err = safePath(change.Path, allowedRoots); err != nil {
			return tx, err
		}
		key := filepath.Clean(change.Path)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return tx, errors.New("duplicate destination in migration")
		}
		seen[key] = true
		actual, e := Snapshot(change.Path)
		if e != nil {
			return tx, e
		}
		if change.BeforeSHA256 == "" || actual != change.BeforeSHA256 {
			return tx, fmt.Errorf("file changed since review: %s", change.Path)
		}
	}
	dir := filepath.Join(m.Root, "transactions", tx.ID)
	if err = safePath(dir, []string{m.Root}); err != nil {
		return tx, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return tx, err
	}
	for i, change := range changes {
		entry := Entry{Path: change.Path, BeforeSHA256: change.BeforeSHA256, AfterSHA256: hash(change.Content)}
		if change.BeforeSHA256 != "absent" {
			if info, e := os.Stat(change.Path); e == nil {
				entry.BeforeMode = uint32(info.Mode().Perm())
			}
			entry.Backup = filepath.Join(dir, fmt.Sprintf("%04d.backup", i))
			data, e := os.ReadFile(change.Path)
			if e != nil {
				return tx, e
			}
			if hash(data) != change.BeforeSHA256 {
				return tx, fmt.Errorf("file changed during backup: %s", change.Path)
			}
			if e = atomicWrite(entry.Backup, data, 0600); e != nil {
				return tx, e
			}
		}
		tx.Entries = append(tx.Entries, entry)
	}
	tx.Status = "applying"
	if err = m.journal(tx); err != nil {
		return tx, err
	}
	for i, change := range changes {
		if err = safePath(change.Path, allowedRoots); err != nil {
			return tx, err
		}
		actual, e := Snapshot(change.Path)
		if e != nil {
			return tx, e
		}
		if actual != change.BeforeSHA256 {
			return tx, fmt.Errorf("file changed during apply: %s", change.Path)
		}
		if err = atomicWrite(change.Path, change.Content, os.FileMode(change.Mode)); err != nil {
			return tx, err
		}
		tx.Entries[i].Applied = true
		if err = m.journal(tx); err != nil {
			return tx, err
		}
	}
	tx.Status = "complete"
	return tx, m.journal(tx)
}

// Recover restores only files still matching our last write. It never destroys later user edits.
func (m *Manager) Recover(transactionID string) error {
	if filepath.Base(transactionID) != transactionID || transactionID == "." || transactionID == "" {
		return errors.New("invalid transaction")
	}
	release, err := m.Acquire()
	if err != nil {
		return err
	}
	defer release()
	journal := filepath.Join(m.Root, "transactions", transactionID, "journal.json")
	if err = safePath(journal, []string{m.Root}); err != nil {
		return err
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		return err
	}
	var tx Transaction
	if err = json.Unmarshal(data, &tx); err != nil {
		return err
	}
	// Validate every entry first so a conflicting recovery does not partially undo other files.
	for _, entry := range tx.Entries {
		if err = safePath(entry.Path, tx.AllowedRoots); err != nil {
			return err
		}
		actual, e := Snapshot(entry.Path)
		if e != nil {
			return e
		}
		if actual != entry.BeforeSHA256 && actual != entry.AfterSHA256 {
			return fmt.Errorf("recovery preserves a later user edit: %s", entry.Path)
		}
		if entry.Backup != "" {
			if err = safePath(entry.Backup, []string{filepath.Dir(journal)}); err != nil {
				return err
			}
			b, e := os.ReadFile(entry.Backup)
			if e != nil || hash(b) != entry.BeforeSHA256 {
				return errors.New("backup failed integrity verification")
			}
		}
	}
	for i := len(tx.Entries) - 1; i >= 0; i-- {
		entry := tx.Entries[i]
		actual, _ := Snapshot(entry.Path)
		if actual == entry.BeforeSHA256 {
			continue
		}
		if entry.BeforeSHA256 == "absent" {
			if err = os.Remove(entry.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else {
			b, _ := os.ReadFile(entry.Backup)
			mode := os.FileMode(entry.BeforeMode)
			if mode == 0 {
				mode = 0600
			}
			if err = atomicWrite(entry.Path, b, mode); err != nil {
				return err
			}
		}
		tx.Entries[i].Applied = false
	}
	tx.Status = "rolled-back"
	return m.journal(tx)
}

func (m *Manager) Status() (StatusInfo, error) {
	s := StatusInfo{Root: m.Root, Transactions: []string{}, Warnings: []string{}}
	if _, err := os.Stat(m.Root); os.IsNotExist(err) {
		return s, nil
	}
	if err := m.EnsureRoot(); err != nil {
		return s, err
	}
	s.Installed = true
	if r, err := m.VerifyRuntime(); err == nil {
		s.Runtime = &r
	} else {
		s.Warnings = append(s.Warnings, err.Error())
	}
	var opts LaunchOptions
	if b, err := os.ReadFile(filepath.Join(m.Root, "launch.json")); err == nil && json.Unmarshal(b, &opts) == nil {
		s.Launch = &opts
	}
	dirs, _ := os.ReadDir(filepath.Join(m.Root, "transactions"))
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		var tx Transaction
		b, e := os.ReadFile(filepath.Join(m.Root, "transactions", d.Name(), "journal.json"))
		if e != nil || json.Unmarshal(b, &tx) != nil {
			continue
		}
		if tx.Status == "applying" {
			s.Transactions = append(s.Transactions, tx.ID)
		}
	}
	if id := m.setupTransaction(); id != "" {
		if tx, err := m.readTransaction(id); err == nil && tx.Status == "complete" {
			s.Setup = id
		}
	}
	return s, nil
}

// MarkSetup records the setup's own transaction. Activations and PATH registration that follow
// are separate transactions, so Undo restores this one and every later one.
func (m *Manager) MarkSetup(id string) error {
	return writeJSON(filepath.Join(m.Root, "setup.json"), map[string]string{"transaction": id})
}

func (m *Manager) setupTransaction() string {
	var record struct {
		Transaction string `json:"transaction"`
	}
	if b, err := os.ReadFile(filepath.Join(m.Root, "setup.json")); err == nil {
		_ = json.Unmarshal(b, &record)
	}
	if filepath.Base(record.Transaction) != record.Transaction {
		return ""
	}
	return record.Transaction
}

func (m *Manager) readTransaction(id string) (Transaction, error) {
	var tx Transaction
	b, err := os.ReadFile(filepath.Join(m.Root, "transactions", id, "journal.json"))
	if err == nil {
		err = json.Unmarshal(b, &tx)
	}
	return tx, err
}

// UndoSetup restores the last setup and everything finished after it, newest first. Recovery
// keeps files edited since, and stops at the first transaction it cannot restore.
func (m *Manager) UndoSetup() error {
	setup := m.setupTransaction()
	if tx, err := m.readTransaction(setup); setup == "" || err != nil || tx.Status != "complete" {
		return errors.New("There's no finished setup to undo")
	}
	dirs, _ := os.ReadDir(filepath.Join(m.Root, "transactions"))
	var later []string
	for _, d := range dirs {
		// IDs start with their UTC time, so name order is time order.
		if tx, err := m.readTransaction(d.Name()); d.IsDir() && d.Name() >= setup && err == nil && tx.Status == "complete" {
			later = append(later, d.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(later)))
	for _, id := range later {
		if err := m.Recover(id); err != nil {
			return err
		}
	}
	return os.Remove(filepath.Join(m.Root, "setup.json"))
}

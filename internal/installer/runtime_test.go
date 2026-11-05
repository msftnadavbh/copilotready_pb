package installer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipBytes(name string, data []byte) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, _ := w.Create(name)
	f.Write(data)
	w.Close()
	return b.Bytes()
}

type runtimeTransport func(*http.Request) (*http.Response, error)

func (f runtimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEnsureRuntimeUpgradesOlderButPreservesNewerAndCorrupt(t *testing.T) {
	asset, err := PlatformAsset()
	if err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct {
		name, version     string
		corrupt, download bool
	}{{"older", "1.0.99", false, true}, {"current", CLIVersion, false, false}, {"newer", "1.0.106", false, false}, {"corrupt", CLIVersion, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			m := New(filepath.Join(home, "kit"), home)
			if err := m.EnsureRoot(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(m.Root, "runtimes", "fixture", asset.Executable)
			if err := atomicWrite(path, []byte("verified"), 0700); err != nil {
				t.Fatal(err)
			}
			rt := Runtime{Version: tc.version, Platform: asset.Platform, Path: path, SHA256: hash([]byte("verified"))}
			if err := writeJSON(filepath.Join(m.Root, "runtime-state.json"), runtimeState{Current: rt}); err != nil {
				t.Fatal(err)
			}
			if tc.corrupt {
				if err := os.WriteFile(path, []byte("corrupt"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			sentinel := errors.New("fixture download reached")
			previous := http.DefaultTransport
			http.DefaultTransport = runtimeTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, sentinel })
			defer func() { http.DefaultTransport = previous }()
			got, err := m.EnsureRuntime(context.Background())
			if called != tc.download {
				t.Fatalf("download attempted=%t, want %t: %v", called, tc.download, err)
			}
			if tc.download && !errors.Is(err, sentinel) {
				t.Fatalf("upgrade did not reach download: %v", err)
			}
			if tc.name == "current" && (err != nil || got.Path != rt.Path) {
				t.Fatalf("current runtime not reused: %v", err)
			}
			if tc.name == "newer" && (err == nil || !strings.Contains(err.Error(), "newer")) {
				t.Fatalf("newer runtime was not protected: %v", err)
			}
			if tc.name == "newer" {
				_, err = m.RepairRuntime(context.Background())
				if called || err == nil || !strings.Contains(err.Error(), "newer") {
					t.Fatalf("repair attempted to downgrade a newer verified runtime: %v", err)
				}
			}
			if tc.corrupt && (err == nil || !strings.Contains(err.Error(), "Repair")) {
				t.Fatalf("corruption did not require repair: %v", err)
			}
		})
	}
}

func TestRepairPreservesVerifiedRollbackWhenCurrentIsCorrupt(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	data := zipBytes("copilot.exe", []byte("verified runtime"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	asset := Asset{Version: "1.0.104", Platform: "win32-x64", URL: server.URL, SHA256: hash(data), Archive: "zip", Executable: "copilot.exe"}
	old, err := m.installAsset(context.Background(), asset, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	asset.Version = CLIVersion
	current, err := m.installAsset(context.Background(), asset, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(current.Path, []byte("corrupt"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = m.installAsset(context.Background(), asset, server.Client()); err != nil {
		t.Fatal(err)
	}
	if err = m.Rollback(); err != nil {
		t.Fatalf("repair lost the verified fallback: %v", err)
	}
	got, err := m.VerifyRuntime()
	if err != nil || got.Path != old.Path {
		t.Fatalf("wrong rollback runtime: %#v, %v", got, err)
	}
}
func TestRuntimeDownloadIntegrityAndTraversal(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	good := zipBytes("copilot.exe", []byte("binary"))
	served := good
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(served) }))
	defer server.Close()
	asset := Asset{Version: "1.0.105", Platform: "win32-x64", URL: server.URL + "/copilot.zip", SHA256: hash(good), Archive: "zip", Executable: "copilot.exe"}
	rt, err := m.installAsset(context.Background(), asset, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if rt.Version != "1.0.105" {
		t.Fatal(rt)
	}
	if _, err = m.VerifyRuntime(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(rt.Path, []byte("changed by native update"), 0700)
	if _, err = m.VerifyRuntime(); err == nil {
		t.Fatal("modified runtime accepted")
	}
	served = []byte("corrupt")
	if _, err = m.installAsset(context.Background(), asset, server.Client()); err == nil {
		t.Fatal("download digest not checked")
	}
	served = zipBytes("../outside", []byte("bad"))
	asset.SHA256 = hash(served)
	if _, err = m.installAsset(context.Background(), asset, server.Client()); err == nil {
		t.Fatal("archive path escaped")
	}
	if _, err = os.Stat(filepath.Join(m.Root, "outside")); !os.IsNotExist(err) {
		t.Fatal("unexpected archive output")
	}
}

package main

import (
	"bytes"
	"copilotready/internal/core"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplyRejectsChangedOrExpiredReviewBeforeSideEffects(t *testing.T) {
	o := options{Mode: "existing", Project: "reviewed-project", Sandbox: "manual"}
	for _, changed := range []string{"plan", "profile", "project", "sandbox", "baseline", "gitAuth", "imports", "overwrites"} {
		t.Run(changed, func(t *testing.T) {
			a := &app{opts: o, planID: "reviewed-plan", selected: []string{"instruction"}, overwrites: []string{"instruction"}, changes: []core.Change{{Path: "must-not-be-written"}}}
			requestOptions := o
			body := map[string]any{"planId": a.planID, "ids": a.selected, "overwrites": a.overwrites}
			switch changed {
			case "plan":
				body["planId"] = "older-tab-plan"
			case "profile":
				requestOptions.Mode = "new"
			case "project":
				requestOptions.Project = "other-project"
			case "sandbox":
				requestOptions.Sandbox = "sandbox"
			case "baseline":
				requestOptions.Baseline = true
			case "gitAuth":
				requestOptions.GitAuth = true
			case "imports":
				body["ids"] = []string{}
			case "overwrites":
				body["overwrites"] = []string{}
			}
			body["options"] = requestOptions
			data, _ := json.Marshal(body)
			// No manager is provided: rejection must happen before any install or file action.
			err := a.apply(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/apply", bytes.NewReader(data)))
			if err == nil || !strings.Contains(err.Error(), "Review the changes again") {
				t.Fatalf("changed review was not rejected: %v", err)
			}
		})
	}
}

func TestLocalActionsRejectForeignPagesAndRetainSettings(t *testing.T) {
	called := false
	h := guard("127.0.0.1:9876", "secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	for _, origin := range []string{"", "https://evil.example"} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:9876/api/apply", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Setup-Token", "secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || called {
			t.Fatal("foreign page accepted")
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:9876/api/apply", nil)
	r.Header.Set("Origin", "http://127.0.0.1:9876")
	r.Header.Set("X-Setup-Token", "secret")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Fatal("valid local action rejected")
	}
	original := []byte("{\n// keep my note\n\"theme\":\"dark\",\"sandbox\":{\"userPolicy\":{\"network\":{\"allowOutbound\":false}}}}")
	patched, err := patchSettings(original, false, true, "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keep my note", "theme", "dark", "allowOutbound", "manual"} {
		if !strings.Contains(string(patched), want) {
			t.Fatalf("lost %q", want)
		}
	}
	if bytes.Equal(original, patched) {
		t.Fatal("settings never applied")
	}
}

func TestPageNamesCursorOnlyWhenTurnedOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		a := &app{token: "page-token", cursor: on}
		rec := httptest.NewRecorder()
		a.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		page := rec.Body.String()
		if strings.Contains(page, "__") || !strings.Contains(page, "page-token") || strings.Contains(page, "Cursor") != on {
			t.Fatalf("cursor=%v: %s", on, page)
		}
	}
}

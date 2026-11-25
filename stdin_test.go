package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type trackedStdin struct {
	io.Reader
	reads, closes int
}

func (s *trackedStdin) Read(p []byte) (int, error) {
	s.reads++
	return s.Reader.Read(p)
}
func (s *trackedStdin) Close() error { s.closes++; return nil }

type stdinFailure struct{}

func (stdinFailure) Read([]byte) (int, error) { return 0, errors.New("input unavailable") }

func TestStdinTargetsPreserveOrderAndOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	first, second, third := server.URL+"/first", server.URL+"/second", server.URL+"/third"
	input := &trackedStdin{Reader: strings.NewReader("\uFEFF# generated targets\r\n\r\n  " + first + " \r\n" + second)}
	var out, diagnostics bytes.Buffer
	code := runWithInput([]string{"-file", "-", "-json", "-concurrency", "2", third}, input, &out, &diagnostics)
	if code != 0 || input.reads == 0 || input.closes != 0 {
		t.Fatalf("exit=%d reads=%d closes=%d diagnostic=%s", code, input.reads, input.closes, &diagnostics)
	}
	decoder := json.NewDecoder(&out)
	for _, want := range []string{first, second, third} {
		var result CheckResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.URL != want || result.State != "UP" {
			t.Fatalf("result=%+v want=%s", result, want)
		}
	}
	var extra CheckResult
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra output: %v", err)
	}
}

func TestInvalidStdinPreventsAllRequests(t *testing.T) {
	requests := make(chan struct{}, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{} }))
	defer server.Close()
	for _, tc := range []struct {
		name  string
		input io.Reader
	}{
		{"empty", strings.NewReader("")},
		{"only comments", strings.NewReader("\uFEFF# comment\r\n\r\n")},
		{"invalid after valid", strings.NewReader(server.URL + "\nftp://example.test\n")},
		{"oversized after valid", strings.NewReader(server.URL + "\n" + strings.Repeat("x", 70*1024))},
		{"read error after valid", io.MultiReader(strings.NewReader(server.URL+"\n"), stdinFailure{})},
		{"UTF16", strings.NewReader("\xff\xfeh\x00t\x00t\x00p\x00")},
		{"BOM after first line", strings.NewReader("# comment\n\uFEFF" + server.URL)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			code := runWithInput([]string{"-file", "-"}, tc.input, &out, &diagnostics)
			if code != 2 || out.Len() != 0 || diagnostics.Len() == 0 || len(requests) != 0 {
				t.Fatalf("exit=%d out=%s diagnostics=%s requests=%d", code, &out, &diagnostics, len(requests))
			}
		})
	}
}

func TestStdinEmptyWithPositionalTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if code := runWithInput([]string{"-file", "-", server.URL}, strings.NewReader(""), &out, &diagnostics); code != 0 {
		t.Fatalf("exit=%d diagnostic=%s", code, &diagnostics)
	}
}

func TestUTF8BOMFilesAndUnselectedStdin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "targets.txt")
	if err := os.WriteFile(path, []byte("\uFEFF"+server.URL+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{server.URL}, {"-file", path}} {
		input := &trackedStdin{Reader: stdinFailure{}}
		var out, diagnostics bytes.Buffer
		if code := runWithInput(args, input, &out, &diagnostics); code != 0 || input.reads != 0 || input.closes != 0 {
			t.Fatalf("exit=%d reads=%d closes=%d diagnostic=%s", code, input.reads, input.closes, &diagnostics)
		}
	}
}

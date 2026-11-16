// Command release cross-compiles the vend CLI for the supported platforms and
// writes a checksum manifest.
//
// Run it from the repository root (the directory containing go.work):
//
//	go run ./tools/release
//
// The command is local-only: it builds into dist/, writes dist/SHA256SUMS and
// never publishes, uploads or touches the network. Builds are reproducible:
// static (CGO disabled), trimmed paths, no VCS stamping and a fixed version.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// releaseVersion is stamped into main.version of the CLI.
	releaseVersion = "0.1.0-m1"
	// packagePath is the CLI entry point, relative to the repository root.
	packagePath = "./core/cmd/vend"
	// distDir is the artifact directory, relative to the repository root.
	distDir = "dist"
	// sumsFile is the checksum manifest name inside distDir.
	sumsFile = "SHA256SUMS"
)

// target is one cross-compilation target and its artifact name.
type target struct {
	goos   string
	goarch string
	output string
}

// targets lists the supported release targets in build order.
var targets = []target{
	{goos: "windows", goarch: "amd64", output: "vend_windows_amd64.exe"},
	{goos: "linux", goarch: "amd64", output: "vend_linux_amd64"},
	{goos: "linux", goarch: "arm64", output: "vend_linux_arm64"},
	{goos: "darwin", goarch: "amd64", output: "vend_darwin_amd64"},
	{goos: "darwin", goarch: "arm64", output: "vend_darwin_arm64"},
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "release: %v\n", err)
		os.Exit(1)
	}
	root, err := findRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "release: %v\n", err)
		os.Exit(1)
	}
	if err := run(root); err != nil {
		fmt.Fprintf(os.Stderr, "release: %v\n", err)
		os.Exit(1)
	}
}

// findRoot walks up from dir until a directory containing go.work is found.
func findRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.work")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("go.work not found above %s; run from the repository root", dir)
		}
		abs = parent
	}
}

// run builds every target and writes the checksum manifest.
// A failed build is reported with its target and stops the run: a manifest is
// only written for a complete set of artifacts.
func run(root string) error {
	if err := os.MkdirAll(filepath.Join(root, distDir), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", distDir, err)
	}
	// Drop a manifest from an earlier run so it never outlives its artifacts.
	if err := removeIfExists(filepath.Join(root, distDir, sumsFile)); err != nil {
		return err
	}

	fmt.Printf("release %s: %d targets -> %s/\n", releaseVersion, len(targets), distDir)
	for _, t := range targets {
		fmt.Printf("  %-6s %-5s %s\n", t.goos, t.goarch, t.output)
		if err := build(root, t); err != nil {
			return err
		}
	}

	if err := writeSums(root); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", filepath.ToSlash(filepath.Join(distDir, sumsFile)))
	return nil
}

// build compiles one target into dist/ and verifies the artifact exists.
func build(root string, t target) error {
	outPath := filepath.Join(root, distDir, t.output)
	// Never leave a stale binary behind if this build fails.
	if err := removeIfExists(outPath); err != nil {
		return fmt.Errorf("build %s/%s: %w", t.goos, t.goarch, err)
	}

	args := []string{
		"build",
		"-trimpath",
		// Keep artifacts identical across checkouts and CI machines.
		"-buildvcs=false",
		"-ldflags=-s -w -X main.version=" + releaseVersion,
		"-o", outPath,
		packagePath,
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = buildEnv(t)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s/%s (%s): %w\n%s",
			t.goos, t.goarch, t.output, err, indent(strings.TrimSpace(output.String())))
	}

	info, err := os.Stat(outPath)
	if err != nil {
		return fmt.Errorf("build %s/%s: artifact %s missing: %w", t.goos, t.goarch, t.output, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("build %s/%s: artifact %s is empty", t.goos, t.goarch, t.output)
	}
	return nil
}

// buildEnv returns the process environment with the cross-compilation
// variables forced for one target.
func buildEnv(t target) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if ok && (key == "CGO_ENABLED" || key == "GOOS" || key == "GOARCH") {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"CGO_ENABLED=0",
		"GOOS="+t.goos,
		"GOARCH="+t.goarch,
	)
}

// writeSums writes dist/SHA256SUMS as "<hex>  <filename>", sorted by filename.
func writeSums(root string) error {
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.output)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	for _, name := range names {
		sum, err := fileSHA256(filepath.Join(root, distDir, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&buf, "%s  %s\n", sum, name)
	}
	if err := os.WriteFile(filepath.Join(root, distDir, sumsFile), buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", sumsFile, err)
	}
	return nil
}

// fileSHA256 returns the lowercase hex SHA-256 of path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checksum %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("checksum %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// removeIfExists deletes path and ignores a missing file.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
	}
	return nil
}

// indent indents captured command output for a nested error message.
func indent(s string) string {
	if s == "" {
		return "  (no output)"
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

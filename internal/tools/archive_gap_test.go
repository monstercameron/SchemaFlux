package tools

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The tool refuses actions it does not recognise, and a "create" with no
// files rather than silently producing an empty archive.
func TestZipToolRefusals(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("unknown action", func(t *testing.T) {
		result, _ := ZipTool.Execute(context.Background(), map[string]any{
			"action": "delete",
			"path":   filepath.Join(tmpDir, "x.zip"),
		})
		if result.Success {
			t.Error("an unrecognised action must not report success")
		}
	})

	t.Run("create with no files", func(t *testing.T) {
		result, _ := ZipTool.Execute(context.Background(), map[string]any{
			"action": "create",
			"path":   filepath.Join(tmpDir, "empty.zip"),
			"files":  []any{},
		})
		if result.Success {
			t.Error("create with an empty files array must be refused")
		}
	})

	t.Run("create with a file that does not exist", func(t *testing.T) {
		result, _ := ZipTool.Execute(context.Background(), map[string]any{
			"action": "create",
			"path":   filepath.Join(tmpDir, "bad.zip"),
			"files":  []any{filepath.Join(tmpDir, "does-not-exist.txt")},
		})
		if result.Success {
			t.Error("create must fail when a source file does not exist")
		}
	})
}

// Extract's own path-traversal guard: a crafted zip entry name that would
// land outside the destination directory must be refused rather than
// written -- this is the check at archive.go:164 and it needs an entry that
// actually attempts to escape, which os.WriteFile-based fixtures cannot
// produce since the standard zip writer will not let a caller write a
// traversal name directly through FileInfoHeader. Build the archive by hand
// instead.
func TestExtractZipRefusesPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	archive := filepath.Join(tmpDir, "evil.zip")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	w := zip.NewWriter(f)
	entry, err := w.Create("../escaped.txt")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := entry.Write([]byte("payload")); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	f.Close()

	dest := filepath.Join(tmpDir, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	result, _ := ZipTool.Execute(context.Background(), map[string]any{
		"action": "extract",
		"path":   archive,
		"dest":   dest,
	})
	if result.Success {
		t.Fatal("a zip entry escaping the destination directory must be refused")
	}
	if !strings.Contains(result.Error, "illegal file path") {
		t.Errorf("the refusal does not explain itself: %q", result.Error)
	}

	if _, statErr := os.Stat(filepath.Join(tmpDir, "escaped.txt")); statErr == nil {
		t.Error("the traversal entry was written outside the destination")
	}
}

// A directory entry inside the archive is recreated as a directory on
// extraction, not written as a zero-byte file.
func TestExtractZipRecreatesDirectoryEntries(t *testing.T) {
	tmpDir := t.TempDir()
	archive := filepath.Join(tmpDir, "withdir.zip")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	w := zip.NewWriter(f)
	if _, err := w.Create("sub/"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fileWriter, err := w.Create("sub/inside.txt")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := fileWriter.Write([]byte("hi")); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	f.Close()

	dest := filepath.Join(tmpDir, "out")
	extracted, err := extractZip(archive, dest)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if len(extracted) != 1 {
		t.Fatalf("expected 1 extracted file (the directory entry is not a file), got %d: %v", len(extracted), extracted)
	}

	info, err := os.Stat(filepath.Join(dest, "sub"))
	if err != nil || !info.IsDir() {
		t.Errorf("sub/ was not recreated as a directory: %v", err)
	}
}

// extractZip on a file that is not a valid ZIP fails rather than producing
// a nonsensical extraction.
func TestExtractZipRefusesAMalformedArchive(t *testing.T) {
	tmpDir := t.TempDir()
	notAZip := filepath.Join(tmpDir, "notazip.zip")
	if err := os.WriteFile(notAZip, []byte("this is not a zip file"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := extractZip(notAZip, tmpDir); err == nil {
		t.Fatal("extractZip accepted a file that is not a valid archive")
	}
}

// addToZip walks a directory tree and adds every file inside it, not just
// the top-level entries.
func TestAddToZipWalksNestedDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src")
	nested := filepath.Join(src, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	archive := filepath.Join(tmpDir, "nested.zip")
	if err := createZip(archive, []string{src}); err != nil {
		t.Fatalf("createZip: %v", err)
	}

	files, err := listZip(archive)
	if err != nil {
		t.Fatalf("listZip: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files from the walk (top.txt and nested/deep.txt), got %d: %+v", len(files), files)
	}
}

// createZip fails cleanly when the target path cannot be created, rather
// than leaving a half-written file behind.
func TestCreateZipFailsWhenThePathIsUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based write refusal is not reliably testable as this user on Windows")
	}
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(testFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := createZip(filepath.Join(tmpDir, "missing-dir", "out.zip"), []string{testFile}); err == nil {
		t.Fatal("createZip succeeded despite the parent directory not existing")
	}
}

// addFileToZip fails when the source no longer exists between the caller's
// stat and the open -- exercised directly since that race is not something
// a fixture can reproduce through the public Execute path, but the function
// still needs to report the open failure rather than panicking.
func TestAddFileToZipFailsOnAMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	archive := filepath.Join(tmpDir, "x.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	defer w.Close()

	if err := addFileToZip(w, filepath.Join(tmpDir, "nope.txt")); err == nil {
		t.Fatal("addFileToZip succeeded on a file that does not exist")
	}
}

// The list action surfaces size, compression, and directory metadata for
// every entry, not just the file names.
func TestListZipReportsEntryMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "data.txt")
	if err := os.WriteFile(testFile, []byte("some content to compress"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	archive := filepath.Join(tmpDir, "meta.zip")
	if err := createZip(archive, []string{testFile}); err != nil {
		t.Fatalf("createZip: %v", err)
	}

	entries, err := listZip(archive)
	if err != nil {
		t.Fatalf("listZip: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0]["is_dir"] != false {
		t.Errorf("is_dir = %v, want false for a plain file", entries[0]["is_dir"])
	}
	if entries[0]["size"].(uint64) == 0 {
		t.Error("size was not reported")
	}
}

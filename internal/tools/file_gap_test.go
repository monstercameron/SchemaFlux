package tools

import (
	"os"
	"path/filepath"
	"testing"

	"context"
)

func TestReadFileToolRefusesAnEmptyPath(t *testing.T) {
	result, _ := ReadFileTool.Execute(context.Background(), map[string]any{})
	if result.Success {
		t.Error("expected failure for a missing path")
	}
}

func TestReadFileToolMissingFile(t *testing.T) {
	result, _ := ReadFileTool.Execute(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "does-not-exist.txt"),
	})
	if result.Success {
		t.Error("expected failure for a missing file")
	}
}

// offset and limit slice the content the same way a caller reading a large
// file in chunks would expect.
func TestReadFileToolOffsetAndLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chunk.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	result, _ := ReadFileTool.Execute(context.Background(), map[string]any{
		"path":   path,
		"offset": float64(2),
		"limit":  float64(3),
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if got := result.Data.(string); got != "234" {
		t.Errorf("content = %q, want %q", got, "234")
	}
}

func TestWriteFileToolRefusesAnEmptyPath(t *testing.T) {
	result, _ := WriteFileTool.Execute(context.Background(), map[string]any{"content": "x"})
	if result.Success {
		t.Error("expected failure for a missing path")
	}
}

func TestWriteFileToolAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "append.txt")

	if _, err := WriteFileTool.Execute(context.Background(), map[string]any{
		"path": path, "content": "first-",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, _ := WriteFileTool.Execute(context.Background(), map[string]any{
		"path": path, "content": "second", "append": true,
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != "first-second" {
		t.Errorf("content = %q, want %q", got, "first-second")
	}
}

// Writing over a path that is itself a directory must fail, rather than
// corrupting the directory or silently doing nothing.
func TestWriteFileRefusesWhenThePathIsADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(dir, "content", false); err == nil {
		t.Error("writing to a directory path must fail")
	}
}

func TestListDirToolRefusesAMissingDirectory(t *testing.T) {
	result, _ := ListDirTool.Execute(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if result.Success {
		t.Error("expected failure for a missing directory")
	}
}

// A pattern filters both the flat and recursive listings, and recursion
// walks into subdirectories the flat listing does not see.
func TestListDirRecursiveWithPattern(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "c.log"), []byte("c"), 0o644)

	result, _ := ListDirTool.Execute(context.Background(), map[string]any{
		"path":      root,
		"recursive": true,
		"pattern":   "*.txt",
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	files := result.Data.([]FileInfo)

	var names []string
	for _, f := range files {
		if !f.IsDir {
			names = append(names, f.Name)
		}
	}
	if len(names) != 2 {
		t.Errorf("expected 2 matching files across the tree, got %v", names)
	}
}

func TestListDirDefaultsToTheCurrentDirectory(t *testing.T) {
	result, _ := ListDirTool.Execute(context.Background(), map[string]any{})
	if !result.Success {
		t.Fatalf("expected success listing the default path: %s", result.Error)
	}
}

func TestCopyFileToolRefusesMissingArguments(t *testing.T) {
	cases := []map[string]any{
		{"dest": "x"},
		{"source": "x"},
	}
	for _, params := range cases {
		result, _ := CopyFileTool.Execute(context.Background(), params)
		if result.Success {
			t.Errorf("%v: expected failure", params)
		}
	}
}

func TestCopyFileToolRefusesAMissingSource(t *testing.T) {
	tmp := t.TempDir()
	result, _ := CopyFileTool.Execute(context.Background(), map[string]any{
		"source": filepath.Join(tmp, "nope.txt"),
		"dest":   filepath.Join(tmp, "out.txt"),
	})
	if result.Success {
		t.Error("copying a nonexistent source must fail")
	}
}

// Copying a directory recurses into it and preserves the tree shape at the
// destination -- exercised through the tool, not just the CopyFile helper
// TestCopyDir already covers directly.
func TestCopyFileToolCopiesADirectoryTree(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	os.MkdirAll(filepath.Join(src, "nested"), 0o755)
	os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0o644)
	os.WriteFile(filepath.Join(src, "nested", "deep.txt"), []byte("deep"), 0o644)

	dest := filepath.Join(tmp, "dst")
	result, _ := CopyFileTool.Execute(context.Background(), map[string]any{
		"source": src,
		"dest":   dest,
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if _, err := os.Stat(filepath.Join(dest, "nested", "deep.txt")); err != nil {
		t.Errorf("nested file did not survive the copy: %v", err)
	}
}

func TestMoveFileToolRefusesMissingArguments(t *testing.T) {
	result, _ := MoveFileTool.Execute(context.Background(), map[string]any{"source": "x"})
	if result.Success {
		t.Error("expected failure for a missing dest")
	}
}

func TestMoveFileToolRefusesAMissingSource(t *testing.T) {
	tmp := t.TempDir()
	result, _ := MoveFileTool.Execute(context.Background(), map[string]any{
		"source": filepath.Join(tmp, "nope.txt"),
		"dest":   filepath.Join(tmp, "out.txt"),
	})
	if result.Success {
		t.Error("moving a nonexistent source must fail")
	}
}

func TestMoveFileToolRenames(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	dst := filepath.Join(tmp, "renamed.txt")
	os.WriteFile(src, []byte("payload"), 0o644)

	result, _ := MoveFileTool.Execute(context.Background(), map[string]any{"source": src, "dest": dst})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the source path should no longer exist after a move")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("the destination should exist after a move: %v", err)
	}
}

func TestDeleteFileToolRefusesAnEmptyPath(t *testing.T) {
	result, _ := DeleteFileTool.Execute(context.Background(), map[string]any{})
	if result.Success {
		t.Error("expected failure for a missing path")
	}
}

// A non-recursive delete refuses a non-empty directory -- the same guard
// os.Remove itself enforces, and the reason a caller must opt into
// "recursive" for anything with contents.
func TestDeleteFileToolRefusesANonEmptyDirectoryWithoutRecursive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "child.txt"), []byte("x"), 0o644)

	result, _ := DeleteFileTool.Execute(context.Background(), map[string]any{"path": dir})
	if result.Success {
		t.Error("deleting a non-empty directory without recursive must fail")
	}
}

func TestDeleteFileToolRecursiveRemovesEverything(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "child.txt"), []byte("x"), 0o644)

	result, _ := DeleteFileTool.Execute(context.Background(), map[string]any{
		"path": dir, "recursive": true,
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the directory should be gone after a recursive delete")
	}
}

func TestFileInfoToolRefusesAMissingPath(t *testing.T) {
	result, _ := FileInfoTool.Execute(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "nope.txt"),
	})
	if result.Success {
		t.Error("expected failure for a missing path")
	}
}

// content-filtering only keeps files whose body contains the search text,
// and Walk errors during the search are what the "count" reports against.
func TestSearchFilesToolFiltersByContent(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "match.txt"), []byte("contains needle here"), 0o644)
	os.WriteFile(filepath.Join(root, "miss.txt"), []byte("nothing interesting"), 0o644)

	result, _ := SearchFilesTool.Execute(context.Background(), map[string]any{
		"path":    root,
		"pattern": "*.txt",
		"content": "needle",
	})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	matches := result.Data.([]FileInfo)
	if len(matches) != 1 || matches[0].Name != "match.txt" {
		t.Errorf("expected only match.txt, got %#v", matches)
	}
}

func TestSearchFilesToolDefaultsToTheCurrentDirectory(t *testing.T) {
	result, _ := SearchFilesTool.Execute(context.Background(), map[string]any{"pattern": "*.go"})
	if !result.Success {
		t.Fatalf("expected success searching the default path: %s", result.Error)
	}
}

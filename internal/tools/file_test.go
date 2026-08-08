package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFile(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "Hello, World!"
	os.WriteFile(testFile, []byte(content), 0644)

	// Test read
	result, err := ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if result != content {
		t.Errorf("Expected %q, got %q", content, result)
	}

	// Test non-existent file
	_, err = ReadFile("/nonexistent/file.txt")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}
}

func TestReadFileTool(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test content"), 0644)

	result, err := ReadFileTool.Execute(context.Background(), map[string]any{
		"path": testFile,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}
	if result.Data != "test content" {
		t.Errorf("Expected 'test content', got %q", result.Data)
	}

	// Test with offset and limit
	result, _ = ReadFileTool.Execute(context.Background(), map[string]any{
		"path":   testFile,
		"offset": 5.0,
		"limit":  4.0,
	})
	if result.Data != "cont" {
		t.Errorf("Expected 'cont', got %q", result.Data)
	}
}

func TestWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "subdir", "test.txt")

	// Test write with directory creation
	err := WriteFile(testFile, "hello", false)
	if err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	content, _ := os.ReadFile(testFile)
	if string(content) != "hello" {
		t.Errorf("Expected 'hello', got %q", string(content))
	}

	// Test append
	err = WriteFile(testFile, " world", true)
	if err != nil {
		t.Fatalf("WriteFile append error: %v", err)
	}

	content, _ = os.ReadFile(testFile)
	if string(content) != "hello world" {
		t.Errorf("Expected 'hello world', got %q", string(content))
	}
}

func TestWriteFileTool(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "write_test.txt")

	result, err := WriteFileTool.Execute(context.Background(), map[string]any{
		"path":    testFile,
		"content": "tool test",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	content, _ := os.ReadFile(testFile)
	if string(content) != "tool test" {
		t.Errorf("Expected 'tool test', got %q", string(content))
	}
}

func TestListDir(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("2"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file3.go"), []byte("3"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)

	// Test basic list
	files, err := listDir(tmpDir, "")
	if err != nil {
		t.Fatalf("listDir error: %v", err)
	}
	if len(files) != 4 {
		t.Errorf("Expected 4 items, got %d", len(files))
	}

	// Test with pattern
	files, err = listDir(tmpDir, "*.txt")
	if err != nil {
		t.Fatalf("listDir error: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("Expected 2 txt files, got %d", len(files))
	}
}

func TestListDirTool(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("test"), 0644)

	result, err := ListDirTool.Execute(context.Background(), map[string]any{
		"path": tmpDir,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	files := result.Data.([]FileInfo)
	if len(files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(files))
	}
}

func TestListDirRecursive(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "root.txt"), []byte("1"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "nested.txt"), []byte("2"), 0644)

	files, err := listDirRecursive(tmpDir, "")
	if err != nil {
		t.Fatalf("listDirRecursive error: %v", err)
	}
	if len(files) < 3 {
		t.Errorf("Expected at least 3 items, got %d", len(files))
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "source.txt")
	dstFile := filepath.Join(tmpDir, "dest.txt")

	os.WriteFile(srcFile, []byte("copy me"), 0644)

	err := CopyFile(srcFile, dstFile)
	if err != nil {
		t.Fatalf("CopyFile error: %v", err)
	}

	content, _ := os.ReadFile(dstFile)
	if string(content) != "copy me" {
		t.Errorf("Expected 'copy me', got %q", string(content))
	}
}

func TestCopyFileTool(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "src.txt")
	dstFile := filepath.Join(tmpDir, "dst.txt")

	os.WriteFile(srcFile, []byte("test"), 0644)

	result, err := CopyFileTool.Execute(context.Background(), map[string]any{
		"source": srcFile,
		"dest":   dstFile,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	if _, err := os.Stat(dstFile); os.IsNotExist(err) {
		t.Error("Destination file not created")
	}
}

func TestMoveFileTool(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "move_src.txt")
	dstFile := filepath.Join(tmpDir, "move_dst.txt")

	os.WriteFile(srcFile, []byte("move me"), 0644)

	result, err := MoveFileTool.Execute(context.Background(), map[string]any{
		"source": srcFile,
		"dest":   dstFile,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Source should not exist
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Error("Source file should not exist after move")
	}

	// Destination should exist
	content, _ := os.ReadFile(dstFile)
	if string(content) != "move me" {
		t.Errorf("Expected 'move me', got %q", string(content))
	}
}

func TestDeleteFileTool(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "delete_me.txt")

	os.WriteFile(testFile, []byte("delete"), 0644)

	result, err := DeleteFileTool.Execute(context.Background(), map[string]any{
		"path": testFile,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("File should not exist after delete")
	}
}

func TestFileExistsTool(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "exists.txt")

	// Test non-existent
	result, _ := FileExistsTool.Execute(context.Background(), map[string]any{
		"path": testFile,
	})
	if result.Data != false {
		t.Error("Expected false for non-existent file")
	}

	// Create file and test again
	os.WriteFile(testFile, []byte("exists"), 0644)
	result, _ = FileExistsTool.Execute(context.Background(), map[string]any{
		"path": testFile,
	})
	if result.Data != true {
		t.Error("Expected true for existing file")
	}
}

func TestFileInfoTool(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "info.txt")
	os.WriteFile(testFile, []byte("info"), 0644)

	result, err := FileInfoTool.Execute(context.Background(), map[string]any{
		"path": testFile,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	info := result.Data.(map[string]any)
	if info["name"] != "info.txt" {
		t.Errorf("Expected name 'info.txt', got %v", info["name"])
	}
	if info["size"].(int64) != 4 {
		t.Errorf("Expected size 4, got %v", info["size"])
	}
	if info["extension"] != ".txt" {
		t.Errorf("Expected extension '.txt', got %v", info["extension"])
	}
}

func TestSearchFilesTool(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("hello world"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("goodbye world"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file3.go"), []byte("package main"), 0644)

	// Search by pattern
	result, _ := SearchFilesTool.Execute(context.Background(), map[string]any{
		"path":    tmpDir,
		"pattern": "*.txt",
	})
	matches := result.Data.([]FileInfo)
	if len(matches) != 2 {
		t.Errorf("Expected 2 txt files, got %d", len(matches))
	}

	// Search with content
	result, _ = SearchFilesTool.Execute(context.Background(), map[string]any{
		"path":    tmpDir,
		"pattern": "*.txt",
		"content": "hello",
	})
	matches = result.Data.([]FileInfo)
	if len(matches) != 1 {
		t.Errorf("Expected 1 file with 'hello', got %d", len(matches))
	}
}

func TestGetMimeType(t *testing.T) {
	tests := []struct {
		ext      string
		expected string
	}{
		{".txt", "text/plain"},
		{".html", "text/html"},
		{".json", "application/json"},
		{".png", "image/png"},
		{".go", "text/x-go"},
		{".unknown", "application/octet-stream"},
	}

	for _, tt := range tests {
		result := getMimeType(tt.ext)
		if result != tt.expected {
			t.Errorf("getMimeType(%q) = %q, expected %q", tt.ext, result, tt.expected)
		}
	}
}

func TestCopyDir(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	dstDir := filepath.Join(tmpDir, "dst")

	os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("root"), 0644)
	os.WriteFile(filepath.Join(srcDir, "subdir", "nested.txt"), []byte("nested"), 0644)

	err := copyDir(srcDir, dstDir)
	if err != nil {
		t.Fatalf("copyDir error: %v", err)
	}

	// Check files exist
	content, _ := os.ReadFile(filepath.Join(dstDir, "file.txt"))
	if string(content) != "root" {
		t.Errorf("Expected 'root', got %q", string(content))
	}

	content, _ = os.ReadFile(filepath.Join(dstDir, "subdir", "nested.txt"))
	if string(content) != "nested" {
		t.Errorf("Expected 'nested', got %q", string(content))
	}
}

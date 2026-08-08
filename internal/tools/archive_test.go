package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestZipToolCreate(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	os.WriteFile(file1, []byte("content1"), 0644)
	os.WriteFile(file2, []byte("content2"), 0644)

	archive := filepath.Join(tmpDir, "test.zip")

	result, _ := ZipTool.Execute(context.Background(), map[string]any{
		"action": "create",
		"path":   archive,
		"files":  []any{file1, file2},
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["file_count"].(int) != 2 {
		t.Errorf("Expected 2 files, got %v", data["file_count"])
	}

	// Verify archive exists
	if _, err := os.Stat(archive); os.IsNotExist(err) {
		t.Error("Archive file not created")
	}
}

func TestZipToolCreateFromDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// Create test files
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("b"), 0644)

	archive := filepath.Join(tmpDir, "test.zip")

	// Use the directory as a single entry in files array
	result, _ := ZipTool.Execute(context.Background(), map[string]any{
		"action": "create",
		"path":   archive,
		"files":  []any{srcDir},
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	// The directory counts as 1 file entry but contains 2 files
	if data["file_count"].(int) != 1 {
		t.Errorf("Expected 1 file entry (directory), got %v", data["file_count"])
	}
}

func TestZipToolExtract(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test content"), 0644)

	archive := filepath.Join(tmpDir, "test.zip")
	ZipTool.Execute(context.Background(), map[string]any{
		"action": "create",
		"path":   archive,
		"files":  []any{testFile},
	})

	// Extract to new directory
	destDir := filepath.Join(tmpDir, "dest")

	result, _ := ZipTool.Execute(context.Background(), map[string]any{
		"action": "extract",
		"path":   archive,
		"dest":   destDir,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	// Verify extracted file - path includes the original path
	data := result.Data.(map[string]any)
	files := data["files"].([]string)
	if len(files) != 1 {
		t.Fatalf("Expected 1 extracted file, got %d", len(files))
	}
}

func TestZipToolList(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	os.WriteFile(file1, []byte("content1"), 0644)
	os.WriteFile(file2, []byte("content2"), 0644)

	archive := filepath.Join(tmpDir, "test.zip")
	ZipTool.Execute(context.Background(), map[string]any{
		"action": "create",
		"path":   archive,
		"files":  []any{file1, file2},
	})

	// List contents
	result, _ := ZipTool.Execute(context.Background(), map[string]any{
		"action": "list",
		"path":   archive,
	})

	if !result.Success {
		t.Fatalf("Expected success: %s", result.Error)
	}

	data := result.Data.(map[string]any)
	if data["file_count"].(int) != 2 {
		t.Errorf("Expected 2 files, got %v", data["file_count"])
	}

	files := data["files"].([]map[string]any)
	if len(files) != 2 {
		t.Error("Expected file list")
	}
}

func TestZipToolInvalidArchive(t *testing.T) {
	result, _ := ZipTool.Execute(context.Background(), map[string]any{
		"action": "list",
		"path":   "/nonexistent/archive.zip",
	})
	if result.Success {
		t.Error("Expected failure for nonexistent archive")
	}
}

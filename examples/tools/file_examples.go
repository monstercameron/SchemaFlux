package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runFileExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📁 FILE TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Create a temp directory for examples
	tempDir := filepath.Join(os.TempDir(), "schemaflux_examples")
	os.MkdirAll(tempDir, 0755)
	defer os.RemoveAll(tempDir)

	// Example 1: Write a file
	testFile := filepath.Join(tempDir, "test.txt")
	result, err := tools.Execute(ctx, "write_file", map[string]any{
		"path":    testFile,
		"content": "Hello, SchemaFlux!\nThis is a test file.\nWith multiple lines.",
	})
	printResult("Write File", result, err)

	// Example 2: Read the file back
	result, err = tools.Execute(ctx, "read_file", map[string]any{
		"path": testFile,
	})
	printResult("Read File", result, err)

	// Example 3: Check file exists
	result, err = tools.Execute(ctx, "file_exists", map[string]any{
		"path": testFile,
	})
	printResult("File Exists (existing)", result, err)

	result, err = tools.Execute(ctx, "file_exists", map[string]any{
		"path": filepath.Join(tempDir, "nonexistent.txt"),
	})
	printResult("File Exists (nonexistent)", result, err)

	// Example 4: Get file info
	result, err = tools.Execute(ctx, "file_info", map[string]any{
		"path": testFile,
	})
	printResult("File Info", result, err)

	// Example 5: List directory
	// First create some more files
	tools.Execute(ctx, "write_file", map[string]any{
		"path":    filepath.Join(tempDir, "file1.go"),
		"content": "package main",
	})
	tools.Execute(ctx, "write_file", map[string]any{
		"path":    filepath.Join(tempDir, "file2.go"),
		"content": "package main",
	})
	tools.Execute(ctx, "write_file", map[string]any{
		"path":    filepath.Join(tempDir, "data.json"),
		"content": `{"key": "value"}`,
	})

	result, err = tools.Execute(ctx, "list_dir", map[string]any{
		"path": tempDir,
	})
	printResult("List Directory", result, err)

	// Example 6: List with pattern filter
	result, err = tools.Execute(ctx, "list_dir", map[string]any{
		"path":    tempDir,
		"pattern": "*.go",
	})
	printResult("List Directory (*.go only)", result, err)

	// Example 7: Copy file
	copyDest := filepath.Join(tempDir, "test_copy.txt")
	result, err = tools.Execute(ctx, "copy_file", map[string]any{
		"source": testFile,
		"dest":   copyDest,
	})
	printResult("Copy File", result, err)

	// Example 8: Move/rename file
	movedFile := filepath.Join(tempDir, "renamed.txt")
	result, err = tools.Execute(ctx, "move_file", map[string]any{
		"source": copyDest,
		"dest":   movedFile,
	})
	printResult("Move/Rename File", result, err)

	// Example 9: Append to file
	result, err = tools.Execute(ctx, "write_file", map[string]any{
		"path":    testFile,
		"content": "\nAppended line!",
		"append":  true,
	})
	printResult("Append to File", result, err)

	// Verify append
	result, err = tools.Execute(ctx, "read_file", map[string]any{
		"path": testFile,
	})
	printResult("Read After Append", result, err)

	// Example 10: Search for files
	result, err = tools.Execute(ctx, "search_files", map[string]any{
		"path":    tempDir,
		"pattern": "*.go",
	})
	printResult("Search Files (*.go)", result, err)

	// Example 11: Search for files containing text
	result, err = tools.Execute(ctx, "search_files", map[string]any{
		"path":    tempDir,
		"pattern": "*.*",
		"content": "SchemaFlux",
	})
	printResult("Search Files (containing 'SchemaFlux')", result, err)

	// Example 12: Create nested directories and list recursively
	nestedDir := filepath.Join(tempDir, "subdir", "nested")
	tools.Execute(ctx, "write_file", map[string]any{
		"path":    filepath.Join(nestedDir, "deep.txt"),
		"content": "Deep file",
	})

	result, err = tools.Execute(ctx, "list_dir", map[string]any{
		"path":      tempDir,
		"recursive": true,
	})
	printResult("List Directory (recursive)", result, err)

	// Example 13: Delete file
	result, err = tools.Execute(ctx, "delete_file", map[string]any{
		"path": movedFile,
	})
	printResult("Delete File", result, err)

	// Example 14: Create a ZIP archive
	zipPath := filepath.Join(tempDir, "archive.zip")
	result, err = tools.Execute(ctx, "zip", map[string]any{
		"action": "create",
		"path":   zipPath,
		"files":  []any{testFile, filepath.Join(tempDir, "file1.go")},
	})
	printResult("Create ZIP Archive", result, err)

	// Example 15: List ZIP contents
	result, err = tools.Execute(ctx, "zip", map[string]any{
		"action": "list",
		"path":   zipPath,
	})
	printResult("List ZIP Contents", result, err)

	// Example 16: Extract ZIP
	extractDir := filepath.Join(tempDir, "extracted")
	result, err = tools.Execute(ctx, "zip", map[string]any{
		"action": "extract",
		"path":   zipPath,
		"dest":   extractDir,
	})
	printResult("Extract ZIP", result, err)

	// Show extracted files
	result, err = tools.Execute(ctx, "list_dir", map[string]any{
		"path": extractDir,
	})
	printResult("Extracted Files", result, err)
}

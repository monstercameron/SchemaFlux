package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileTool reads file contents.
var ReadFileTool = &Tool{
	Name:        "read_file",
	Description: "Read contents of a file",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path":   StringParam("File path to read"),
		"offset": NumberParam("Start reading from this byte offset (optional)"),
		"limit":  NumberParam("Maximum bytes to read (optional)"),
	}, []string{"path"}),
	Execute: executeReadFile,
}

func executeReadFile(ctx context.Context, params map[string]any) (Result, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return ErrorResultFromError(fmt.Errorf("path is required")), nil
	}

	content, err := ReadFile(path)
	if err != nil {
		return ErrorResult(err), nil
	}

	// Handle offset and limit
	offset := 0
	if o, ok := params["offset"].(float64); ok {
		offset = int(o)
	}
	limit := len(content)
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
	}

	if offset > 0 && offset < len(content) {
		content = content[offset:]
	}
	if limit > 0 && limit < len(content) {
		content = content[:limit]
	}

	info, _ := os.Stat(path)
	meta := map[string]any{"path": path}
	if info != nil {
		meta["size"] = info.Size()
		meta["modified"] = info.ModTime()
		meta["is_dir"] = info.IsDir()
	}

	return NewResultWithMeta(content, meta), nil
}

// ReadFile reads a file and returns its contents.
func ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFileTool writes content to a file.
var WriteFileTool = &Tool{
	Name:        "write_file",
	Description: "Write content to a file (creates directories if needed)",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path":    StringParam("File path to write"),
		"content": StringParam("Content to write"),
		"append":  BoolParam("Append to file instead of overwriting"),
	}, []string{"path", "content"}),
	Execute: executeWriteFile,
}

func executeWriteFile(ctx context.Context, params map[string]any) (Result, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)
	appendMode, _ := params["append"].(bool)

	if path == "" {
		return ErrorResultFromError(fmt.Errorf("path is required")), nil
	}

	err := WriteFile(path, content, appendMode)
	if err != nil {
		return ErrorResult(err), nil
	}

	info, _ := os.Stat(path)
	meta := map[string]any{
		"path":   path,
		"append": appendMode,
	}
	if info != nil {
		meta["size"] = info.Size()
	}

	return NewResultWithMeta("file written successfully", meta), nil
}

// WriteFile writes content to a file.
func WriteFile(path, content string, append bool) error {
	// Create directories if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var file *os.File
	var err error

	if append {
		file, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	} else {
		file, err = os.Create(path)
	}
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(content)
	return err
}

// ListDirTool lists directory contents.
var ListDirTool = &Tool{
	Name:        "list_dir",
	Description: "List files and directories in a path",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path":      StringParam("Directory path to list"),
		"recursive": BoolParam("List recursively"),
		"pattern":   StringParam("Glob pattern to filter (e.g., '*.txt')"),
	}, []string{"path"}),
	Execute: executeListDir,
}

// FileInfo represents file metadata.
type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
}

func executeListDir(ctx context.Context, params map[string]any) (Result, error) {
	path, _ := params["path"].(string)
	recursive, _ := params["recursive"].(bool)
	pattern, _ := params["pattern"].(string)

	if path == "" {
		path = "."
	}

	var files []FileInfo
	var err error

	if recursive {
		files, err = listDirRecursive(path, pattern)
	} else {
		files, err = listDir(path, pattern)
	}

	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta(files, map[string]any{
		"path":      path,
		"count":     len(files),
		"recursive": recursive,
	}), nil
}

func listDir(path, pattern string) ([]FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	var files []FileInfo
	for _, entry := range entries {
		if pattern != "" {
			matched, _ := filepath.Match(pattern, entry.Name())
			if !matched {
				continue
			}
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, FileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(path, entry.Name()),
			Size:    info.Size(),
			IsDir:   entry.IsDir(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	return files, nil
}

func listDirRecursive(root, pattern string) ([]FileInfo, error) {
	var files []FileInfo

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if pattern != "" {
			matched, _ := filepath.Match(pattern, info.Name())
			if !matched && !info.IsDir() {
				return nil
			}
		}

		files = append(files, FileInfo{
			Name:    info.Name(),
			Path:    path,
			Size:    info.Size(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})

		return nil
	})

	return files, err
}

// CopyFileTool copies files or directories.
var CopyFileTool = &Tool{
	Name:        "copy_file",
	Description: "Copy a file or directory",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"source": StringParam("Source path"),
		"dest":   StringParam("Destination path"),
	}, []string{"source", "dest"}),
	Execute: executeCopyFile,
}

func executeCopyFile(ctx context.Context, params map[string]any) (Result, error) {
	source, _ := params["source"].(string)
	dest, _ := params["dest"].(string)

	if source == "" || dest == "" {
		return ErrorResultFromError(fmt.Errorf("source and dest are required")), nil
	}

	err := CopyFile(source, dest)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta("copied successfully", map[string]any{
		"source": source,
		"dest":   dest,
	}), nil
}

// CopyFile copies a file.
func CopyFile(source, dest string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return copyDir(source, dest)
	}

	return copyFileContents(source, dest)
}

func copyFileContents(source, dest string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()

	// Create destination directory if needed
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	dst, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func copyDir(source, dest string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(source, path)
		destPath := filepath.Join(dest, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		return copyFileContents(path, destPath)
	})
}

// MoveFileTool moves/renames files.
var MoveFileTool = &Tool{
	Name:        "move_file",
	Description: "Move or rename a file or directory",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"source": StringParam("Source path"),
		"dest":   StringParam("Destination path"),
	}, []string{"source", "dest"}),
	Execute: executeMoveFile,
}

func executeMoveFile(ctx context.Context, params map[string]any) (Result, error) {
	source, _ := params["source"].(string)
	dest, _ := params["dest"].(string)

	if source == "" || dest == "" {
		return ErrorResultFromError(fmt.Errorf("source and dest are required")), nil
	}

	// Create destination directory if needed
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return ErrorResult(err), nil
	}

	err := os.Rename(source, dest)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta("moved successfully", map[string]any{
		"source": source,
		"dest":   dest,
	}), nil
}

// DeleteFileTool deletes files or directories.
var DeleteFileTool = &Tool{
	Name:        "delete_file",
	Description: "Delete a file or directory",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path":      StringParam("Path to delete"),
		"recursive": BoolParam("Delete directories recursively"),
	}, []string{"path"}),
	Execute: executeDeleteFile,
}

func executeDeleteFile(ctx context.Context, params map[string]any) (Result, error) {
	path, _ := params["path"].(string)
	recursive, _ := params["recursive"].(bool)

	if path == "" {
		return ErrorResultFromError(fmt.Errorf("path is required")), nil
	}

	var err error
	if recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}

	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta("deleted successfully", map[string]any{
		"path":      path,
		"recursive": recursive,
	}), nil
}

// FileExistsTool checks if a file exists.
var FileExistsTool = &Tool{
	Name:        "file_exists",
	Description: "Check if a file or directory exists",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path": StringParam("Path to check"),
	}, []string{"path"}),
	Execute: executeFileExists,
}

func executeFileExists(ctx context.Context, params map[string]any) (Result, error) {
	path, _ := params["path"].(string)

	info, err := os.Stat(path)
	exists := err == nil

	meta := map[string]any{
		"path":   path,
		"exists": exists,
	}
	if exists {
		meta["is_dir"] = info.IsDir()
		meta["size"] = info.Size()
	}

	return NewResultWithMeta(exists, meta), nil
}

// FileInfoTool gets detailed file info.
var FileInfoTool = &Tool{
	Name:        "file_info",
	Description: "Get detailed information about a file",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path": StringParam("Path to get info for"),
	}, []string{"path"}),
	Execute: executeFileInfo,
}

func executeFileInfo(ctx context.Context, params map[string]any) (Result, error) {
	path, _ := params["path"].(string)

	info, err := os.Stat(path)
	if err != nil {
		return ErrorResult(err), nil
	}

	ext := filepath.Ext(path)
	mimeType := getMimeType(ext)

	return NewResult(map[string]any{
		"path":      path,
		"name":      info.Name(),
		"size":      info.Size(),
		"is_dir":    info.IsDir(),
		"mode":      info.Mode().String(),
		"mod_time":  info.ModTime().Format("2006-01-02 15:04:05"),
		"extension": ext,
		"mime_type": mimeType,
	}), nil
}

func getMimeType(ext string) string {
	ext = strings.ToLower(ext)
	mimeTypes := map[string]string{
		".txt":  "text/plain",
		".html": "text/html",
		".css":  "text/css",
		".js":   "application/javascript",
		".json": "application/json",
		".xml":  "application/xml",
		".pdf":  "application/pdf",
		".zip":  "application/zip",
		".gz":   "application/gzip",
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".svg":  "image/svg+xml",
		".mp3":  "audio/mpeg",
		".mp4":  "video/mp4",
		".go":   "text/x-go",
		".py":   "text/x-python",
		".rs":   "text/x-rust",
		".md":   "text/markdown",
	}
	if mime, ok := mimeTypes[ext]; ok {
		return mime
	}
	return "application/octet-stream"
}

// SearchFilesTool searches for files by name pattern.
var SearchFilesTool = &Tool{
	Name:        "search_files",
	Description: "Search for files matching a pattern",
	Category:    CategoryFile,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path":    StringParam("Root path to search"),
		"pattern": StringParam("Glob pattern (e.g., '*.go')"),
		"content": StringParam("Search for files containing this text"),
	}, []string{"path", "pattern"}),
	Execute: executeSearchFiles,
}

func executeSearchFiles(ctx context.Context, params map[string]any) (Result, error) {
	root, _ := params["path"].(string)
	pattern, _ := params["pattern"].(string)
	content, _ := params["content"].(string)

	if root == "" {
		root = "."
	}

	var matches []FileInfo

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}

		matched, _ := filepath.Match(pattern, info.Name())
		if !matched {
			return nil
		}

		// Check content if specified
		if content != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if !strings.Contains(string(data), content) {
				return nil
			}
		}

		matches = append(matches, FileInfo{
			Name:    info.Name(),
			Path:    path,
			Size:    info.Size(),
			IsDir:   false,
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})

		return nil
	})

	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta(matches, map[string]any{
		"root":    root,
		"pattern": pattern,
		"count":   len(matches),
	}), nil
}

func init() {
	mustRegister(ReadFileTool)
	mustRegister(WriteFileTool)
	mustRegister(ListDirTool)
	mustRegister(CopyFileTool)
	mustRegister(MoveFileTool)
	mustRegister(DeleteFileTool)
	mustRegister(FileExistsTool)
	mustRegister(FileInfoTool)
	mustRegister(SearchFilesTool)
}

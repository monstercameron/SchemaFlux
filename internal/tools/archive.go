package tools

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ZipTool creates and extracts ZIP archives
var ZipTool = &Tool{
	Name:        "zip",
	Description: "Create or extract ZIP archives.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action to perform", []string{"create", "extract", "list"}),
		"path":   StringParam("Path to ZIP file"),
		"files":  {Type: "array", Description: "Files to add (for create)"},
		"dest":   StringParam("Destination directory (for extract)"),
	}, []string{"action", "path"}),
	Execute: executeZip,
}

func executeZip(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	path, _ := params["path"].(string)

	switch action {
	case "create":
		filesParam, _ := params["files"].([]any)
		if len(filesParam) == 0 {
			return ErrorResultFromError(fmt.Errorf("files array is required")), nil
		}

		var files []string
		for _, f := range filesParam {
			files = append(files, fmt.Sprint(f))
		}

		if err := createZip(path, files); err != nil {
			return ErrorResultFromError(fmt.Errorf("zip creation failed: %w", err)), nil
		}

		return NewResultWithMeta(map[string]any{
			"path":       path,
			"file_count": len(files),
		}, nil), nil

	case "extract":
		dest, _ := params["dest"].(string)
		if dest == "" {
			dest = "."
		}

		files, err := extractZip(path, dest)
		if err != nil {
			return ErrorResultFromError(fmt.Errorf("zip extraction failed: %w", err)), nil
		}

		return NewResultWithMeta(map[string]any{
			"destination": dest,
			"files":       files,
			"file_count":  len(files),
		}, nil), nil

	case "list":
		files, err := listZip(path)
		if err != nil {
			return ErrorResultFromError(fmt.Errorf("zip list failed: %w", err)), nil
		}

		return NewResultWithMeta(map[string]any{
			"files":      files,
			"file_count": len(files),
		}, nil), nil

	default:
		return ErrorResultFromError(fmt.Errorf("action must be 'create', 'extract', or 'list'")), nil
	}
}

func createZip(zipPath string, files []string) error {
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	w := zip.NewWriter(zipFile)
	defer w.Close()

	for _, file := range files {
		if err := addToZip(w, file); err != nil {
			return fmt.Errorf("failed to add %s: %w", file, err)
		}
	}

	return nil
}

func addToZip(w *zip.Writer, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			return addFileToZip(w, filePath)
		})
	}

	return addFileToZip(w, path)
}

func addFileToZip(w *zip.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	// Use just the base name to avoid full path issues
	header.Name = filepath.Base(path)
	header.Method = zip.Deflate

	writer, err := w.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, file)
	return err
}

func extractZip(zipPath, dest string) ([]string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var extracted []string

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		// Security check
		if !strings.HasPrefix(filepath.Clean(fpath), filepath.Clean(dest)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return nil, err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return nil, err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return nil, err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return nil, err
		}

		extracted = append(extracted, fpath)
	}

	return extracted, nil
}

func listZip(zipPath string) ([]map[string]any, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var files []map[string]any
	for _, f := range r.File {
		files = append(files, map[string]any{
			"name":       f.Name,
			"size":       f.UncompressedSize64,
			"compressed": f.CompressedSize64,
			"is_dir":     f.FileInfo().IsDir(),
			"modified":   f.Modified.Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	return files, nil
}

func init() {
	mustRegister(ZipTool)
}

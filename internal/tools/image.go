package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VisionTool analyzes images using AI vision (stub - requires AI vision API)
var VisionTool = &Tool{
	Name:         "vision",
	Description:  "Analyze images using AI vision (stub - requires OpenAI/Claude vision API)",
	Category:     CategoryVision,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"image":  StringParam("Image URL or base64-encoded image"),
		"prompt": StringParam("Question or instruction about the image"),
		"detail": EnumParam("Detail level", []string{"low", "high", "auto"}),
	}, []string{"image", "prompt"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		prompt, _ := params["prompt"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"prompt":  prompt,
			"message": "Vision analysis requires AI vision API integration (OpenAI, Claude, etc.)",
		}, map[string]any{"stubbed": true}), nil
	},
}

// OCRTool extracts text from images (stub - requires OCR service)
var OCRTool = &Tool{
	Name:        "ocr",
	Description: "Extract text from images using OCR (stub - requires Tesseract or cloud OCR)",
	Category:    CategoryVision,
	IsStub:      true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"image":    StringParam("Image file path or base64-encoded image"),
		"language": StringParam("OCR language code (e.g., 'eng', 'fra')"),
	}, []string{"image"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"message": "OCR requires Tesseract or cloud OCR service integration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// ImageInfoTool gets image metadata and dimensions
var ImageInfoTool = &Tool{
	Name:        "image_info",
	Description: "Get image metadata including dimensions and format.",
	Category:    CategoryVision,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"path": StringParam("Path to image file"),
	}, []string{"path"}),
	Execute: executeImageInfo,
}

func executeImageInfo(ctx context.Context, params map[string]any) (Result, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return ErrorResultFromError(fmt.Errorf("path is required")), nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return ErrorResultFromError(fmt.Errorf("cannot access file: %w", err)), nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	format := "unknown"
	switch ext {
	case ".jpg", ".jpeg":
		format = "jpeg"
	case ".png":
		format = "png"
	case ".gif":
		format = "gif"
	case ".webp":
		format = "webp"
	case ".bmp":
		format = "bmp"
	case ".svg":
		format = "svg"
	}

	return NewResultWithMeta(map[string]any{
		"path":      path,
		"format":    format,
		"extension": ext,
		"size":      info.Size(),
		"modified":  info.ModTime().Format("2006-01-02T15:04:05Z07:00"),
	}, nil), nil
}

// ImageResizeTool resizes images (stub - requires image processing library)
var ImageResizeTool = &Tool{
	Name:        "image_resize",
	Description: "Resize images (stub - requires image processing library)",
	Category:    CategoryVision,
	IsStub:      true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"input":       StringParam("Input image path"),
		"output":      StringParam("Output image path"),
		"width":       NumberParam("Target width in pixels"),
		"height":      NumberParam("Target height in pixels"),
		"keep_aspect": BoolParam("Maintain aspect ratio"),
	}, []string{"input", "width"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"message": "Image resizing requires image processing library",
		}, map[string]any{"stubbed": true}), nil
	},
}

// ImageCropTool crops images (stub - requires image processing library)
var ImageCropTool = &Tool{
	Name:        "image_crop",
	Description: "Crop images (stub - requires image processing library)",
	Category:    CategoryVision,
	IsStub:      true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"input":  StringParam("Input image path"),
		"output": StringParam("Output image path"),
		"x":      NumberParam("X coordinate of top-left corner"),
		"y":      NumberParam("Y coordinate of top-left corner"),
		"width":  NumberParam("Crop width"),
		"height": NumberParam("Crop height"),
	}, []string{"input", "x", "y", "width", "height"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"message": "Image cropping requires image processing library",
		}, map[string]any{"stubbed": true}), nil
	},
}

// ImageConvertTool converts between image formats (stub)
var ImageConvertTool = &Tool{
	Name:        "image_convert",
	Description: "Convert images between formats (stub - requires image processing library)",
	Category:    CategoryVision,
	IsStub:      true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"input":   StringParam("Input image path"),
		"output":  StringParam("Output image path"),
		"format":  EnumParam("Target format", []string{"png", "jpeg", "gif", "webp", "bmp"}),
		"quality": NumberParam("Quality for lossy formats (1-100)"),
	}, []string{"input", "format"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"message": "Image conversion requires image processing library",
		}, map[string]any{"stubbed": true}), nil
	},
}

// ImageBase64Tool encodes/decodes images as base64
var ImageBase64Tool = &Tool{
	Name:        "image_base64",
	Description: "Encode images to base64 or decode base64 to images.",
	Category:    CategoryVision,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action to perform", []string{"encode", "decode"}),
		"path":   StringParam("Image file path"),
		"data":   StringParam("Base64 data (for decode)"),
	}, []string{"action"}),
	Execute: executeImageBase64,
}

func executeImageBase64(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)

	switch action {
	case "encode":
		path, _ := params["path"].(string)
		if path == "" {
			return ErrorResultFromError(fmt.Errorf("path is required for encode")), nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return ErrorResultFromError(fmt.Errorf("cannot read file: %w", err)), nil
		}

		encoded := base64.StdEncoding.EncodeToString(data)
		ext := strings.ToLower(filepath.Ext(path))
		mimeType := "application/octet-stream"
		switch ext {
		case ".jpg", ".jpeg":
			mimeType = "image/jpeg"
		case ".png":
			mimeType = "image/png"
		case ".gif":
			mimeType = "image/gif"
		case ".webp":
			mimeType = "image/webp"
		}

		return NewResultWithMeta(map[string]any{
			"base64":    encoded,
			"data_uri":  fmt.Sprintf("data:%s;base64,%s", mimeType, encoded),
			"mime_type": mimeType,
			"size":      len(data),
		}, nil), nil

	case "decode":
		data, _ := params["data"].(string)
		path, _ := params["path"].(string)
		if data == "" || path == "" {
			return ErrorResultFromError(fmt.Errorf("data and path are required for decode")), nil
		}

		// Remove data URI prefix if present
		if strings.HasPrefix(data, "data:") {
			if idx := strings.Index(data, ","); idx != -1 {
				data = data[idx+1:]
			}
		}

		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return ErrorResultFromError(fmt.Errorf("invalid base64: %w", err)), nil
		}

		if err := os.WriteFile(path, decoded, 0644); err != nil {
			return ErrorResultFromError(fmt.Errorf("cannot write file: %w", err)), nil
		}

		return NewResultWithMeta(map[string]any{
			"path": path,
			"size": len(decoded),
		}, nil), nil

	default:
		return ErrorResultFromError(fmt.Errorf("action must be 'encode' or 'decode'")), nil
	}
}

// ThumbnailTool generates thumbnails (stub)
var ThumbnailTool = &Tool{
	Name:        "thumbnail",
	Description: "Generate image thumbnails (stub - requires image processing library)",
	Category:    CategoryVision,
	IsStub:      true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"input":  StringParam("Input image path"),
		"output": StringParam("Output thumbnail path"),
		"size":   NumberParam("Maximum dimension (width or height)"),
	}, []string{"input", "size"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"message": "Thumbnail generation requires image processing library",
		}, map[string]any{"stubbed": true}), nil
	},
}

func init() {
	mustRegister(VisionTool)
	mustRegister(OCRTool)
	mustRegister(ImageInfoTool)
	mustRegister(ImageResizeTool)
	mustRegister(ImageCropTool)
	mustRegister(ImageConvertTool)
	mustRegister(ImageBase64Tool)
	mustRegister(ThumbnailTool)
}

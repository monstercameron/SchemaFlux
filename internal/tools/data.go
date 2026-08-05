package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"text/tabwriter"
)

// CSVTool parses and generates CSV data
var CSVTool = &Tool{
	Name:        "csv",
	Description: "Parse and generate CSV data. Supports reading CSV to structured data and writing data to CSV format.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action":      EnumParam("Action to perform", []string{"parse", "format"}),
		"data":        StringParam("CSV string to parse (for parse action)"),
		"rows":        {Type: "array", Description: "Array of arrays or objects to format as CSV"},
		"headers":     {Type: "array", Description: "Column headers (optional)"},
		"delimiter":   StringParam("Field delimiter (default: ',')"),
		"skip_header": BoolParam("Skip first row when parsing"),
	}, []string{"action"}),
	Execute: executeCSV,
}

func executeCSV(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	delimiter := ','
	if d, ok := params["delimiter"].(string); ok && len(d) > 0 {
		delimiter = rune(d[0])
	}

	switch action {
	case "parse":
		data, _ := params["data"].(string)
		if data == "" {
			return ErrorResultFromError(fmt.Errorf("data is required for parse action")), nil
		}

		reader := csv.NewReader(strings.NewReader(data))
		reader.Comma = delimiter
		reader.FieldsPerRecord = -1

		records, err := reader.ReadAll()
		if err != nil {
			return ErrorResultFromError(fmt.Errorf("CSV parse error: %w", err)), nil
		}

		if len(records) == 0 {
			return NewResultWithMeta([][]string{}, map[string]any{"message": "Empty CSV"}), nil
		}

		headers := records[0]
		var rows []map[string]any
		for i := 1; i < len(records); i++ {
			row := make(map[string]any)
			for j, val := range records[i] {
				if j < len(headers) {
					row[headers[j]] = val
				}
			}
			rows = append(rows, row)
		}

		return NewResultWithMeta(rows, map[string]any{
			"row_count": len(rows),
			"headers":   headers,
		}), nil

	case "format":
		rows, ok := params["rows"].([]any)
		if !ok || len(rows) == 0 {
			return ErrorResultFromError(fmt.Errorf("rows array is required")), nil
		}

		var headers []string
		if h, ok := params["headers"].([]any); ok {
			for _, v := range h {
				headers = append(headers, fmt.Sprint(v))
			}
		}

		var buf bytes.Buffer
		writer := csv.NewWriter(&buf)
		writer.Comma = delimiter

		if len(headers) == 0 {
			if obj, ok := rows[0].(map[string]any); ok {
				for k := range obj {
					headers = append(headers, k)
				}
				sort.Strings(headers)
			}
		}

		if len(headers) > 0 {
			writer.Write(headers)
		}

		for _, row := range rows {
			var record []string
			switch r := row.(type) {
			case []any:
				for _, v := range r {
					record = append(record, fmt.Sprint(v))
				}
			case map[string]any:
				for _, h := range headers {
					record = append(record, fmt.Sprint(r[h]))
				}
			}
			writer.Write(record)
		}

		writer.Flush()
		return NewResultWithMeta(buf.String(), map[string]any{"row_count": len(rows)}), nil

	default:
		return ErrorResultFromError(fmt.Errorf("action must be 'parse' or 'format'")), nil
	}
}

// JSONTool parses and generates JSON data
var JSONTool = &Tool{
	Name:        "json",
	Description: "Parse and generate JSON data with path extraction and validation.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action to perform", []string{"parse", "format", "extract", "validate"}),
		"data":   StringParam("JSON string to parse or validate"),
		"object": {Type: "object", Description: "Object to format as JSON"},
		"path":   StringParam("JSONPath expression (e.g., 'users.0.name')"),
		"pretty": BoolParam("Pretty print output"),
	}, []string{"action"}),
	Execute: executeJSON,
}

func executeJSON(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	pretty := true
	if p, ok := params["pretty"].(bool); ok {
		pretty = p
	}

	switch action {
	case "parse":
		data, _ := params["data"].(string)
		if data == "" {
			return ErrorResultFromError(fmt.Errorf("data is required")), nil
		}

		var result any
		if err := json.Unmarshal([]byte(data), &result); err != nil {
			return ErrorResultFromError(fmt.Errorf("JSON parse error: %w", err)), nil
		}
		return NewResultWithMeta(result, map[string]any{"type": fmt.Sprintf("%T", result)}), nil

	case "format":
		obj := params["object"]
		if obj == nil {
			return ErrorResultFromError(fmt.Errorf("object is required")), nil
		}

		var output []byte
		var err error
		if pretty {
			output, err = json.MarshalIndent(obj, "", "  ")
		} else {
			output, err = json.Marshal(obj)
		}
		if err != nil {
			return ErrorResultFromError(fmt.Errorf("JSON format error: %w", err)), nil
		}
		return NewResultWithMeta(string(output), map[string]any{"pretty": pretty}), nil

	case "extract":
		data, _ := params["data"].(string)
		path, _ := params["path"].(string)
		if data == "" || path == "" {
			return ErrorResultFromError(fmt.Errorf("data and path are required")), nil
		}

		var obj any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			return ErrorResultFromError(fmt.Errorf("JSON parse error: %w", err)), nil
		}

		result, err := extractJSONPath(obj, path)
		if err != nil {
			return ErrorResult(err), nil
		}
		return NewResultWithMeta(result, map[string]any{"path": path}), nil

	case "validate":
		data, _ := params["data"].(string)
		var result any
		err := json.Unmarshal([]byte(data), &result)
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		return NewResultWithMeta(map[string]any{"valid": err == nil, "error": errMsg}, nil), nil

	default:
		return ErrorResultFromError(fmt.Errorf("invalid action")), nil
	}
}

func extractJSONPath(obj any, path string) (any, error) {
	parts := strings.Split(path, ".")
	current := obj

	for _, part := range parts {
		if part == "" {
			continue
		}
		switch v := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = v[part]
			if !ok {
				return nil, fmt.Errorf("key '%s' not found", part)
			}
		case []any:
			var idx int
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil {
				return nil, fmt.Errorf("array index expected")
			}
			if idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("index out of bounds")
			}
			current = v[idx]
		default:
			return nil, fmt.Errorf("cannot navigate into %T", v)
		}
	}
	return current, nil
}

// XMLTool parses and generates XML data
var XMLTool = &Tool{
	Name:        "xml",
	Description: "Parse and generate XML data.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action to perform", []string{"parse", "format"}),
		"data":   StringParam("XML string to parse"),
		"object": {Type: "object", Description: "Object to format as XML"},
		"root":   StringParam("Root element name (default: 'root')"),
	}, []string{"action"}),
	Execute: executeXML,
}

func executeXML(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)

	switch action {
	case "parse":
		data, _ := params["data"].(string)
		if data == "" {
			return ErrorResultFromError(fmt.Errorf("data is required")), nil
		}
		result, err := parseXML(data)
		if err != nil {
			return ErrorResult(err), nil
		}
		return NewResultWithMeta(result, nil), nil

	case "format":
		obj := params["object"]
		if obj == nil {
			return ErrorResultFromError(fmt.Errorf("object is required")), nil
		}
		root := "root"
		if r, ok := params["root"].(string); ok && r != "" {
			root = r
		}
		output := formatXML(obj, root, true, 0)
		return NewResultWithMeta(output, map[string]any{"root": root}), nil

	default:
		return ErrorResultFromError(fmt.Errorf("invalid action")), nil
	}
}

func parseXML(data string) (any, error) {
	decoder := xml.NewDecoder(strings.NewReader(data))
	return parseXMLToken(decoder)
}

func parseXMLToken(decoder *xml.Decoder) (any, error) {
	result := make(map[string]any)
	var currentKey string
	var textContent strings.Builder

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := token.(type) {
		case xml.StartElement:
			currentKey = t.Name.Local
			child, err := parseXMLToken(decoder)
			if err != nil {
				return nil, err
			}
			if existing, ok := result[currentKey]; ok {
				if arr, ok := existing.([]any); ok {
					result[currentKey] = append(arr, child)
				} else {
					result[currentKey] = []any{existing, child}
				}
			} else {
				result[currentKey] = child
			}
		case xml.EndElement:
			if len(result) == 0 {
				content := strings.TrimSpace(textContent.String())
				if content != "" {
					return content, nil
				}
			}
			return result, nil
		case xml.CharData:
			textContent.Write(t)
		}
	}
	return result, nil
}

func formatXML(obj any, name string, pretty bool, depth int) string {
	var buf strings.Builder
	indent := ""
	newline := ""
	if pretty {
		indent = strings.Repeat("  ", depth)
		newline = "\n"
	}

	switch v := obj.(type) {
	case map[string]any:
		buf.WriteString(fmt.Sprintf("%s<%s>%s", indent, name, newline))
		for k, val := range v {
			buf.WriteString(formatXML(val, k, pretty, depth+1))
		}
		buf.WriteString(fmt.Sprintf("%s</%s>%s", indent, name, newline))
	case []any:
		for _, item := range v {
			buf.WriteString(formatXML(item, name, pretty, depth))
		}
	default:
		buf.WriteString(fmt.Sprintf("%s<%s>%v</%s>%s", indent, name, v, name, newline))
	}
	return buf.String()
}

// TableTool formats data as text tables
var TableTool = &Tool{
	Name:        "table",
	Description: "Format data as text, markdown, or HTML tables.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"data":    {Type: "array", Description: "Array of objects or arrays"},
		"headers": {Type: "array", Description: "Column headers"},
		"format":  EnumParam("Output format", []string{"text", "markdown", "html"}),
	}, []string{"data"}),
	Execute: executeTable,
}

func executeTable(ctx context.Context, params map[string]any) (Result, error) {
	data, ok := params["data"].([]any)
	if !ok || len(data) == 0 {
		return ErrorResultFromError(fmt.Errorf("data array is required")), nil
	}

	format := "text"
	if f, ok := params["format"].(string); ok {
		format = f
	}

	var headers []string
	if h, ok := params["headers"].([]any); ok {
		for _, v := range h {
			headers = append(headers, fmt.Sprint(v))
		}
	}

	if len(headers) == 0 {
		if obj, ok := data[0].(map[string]any); ok {
			for k := range obj {
				headers = append(headers, k)
			}
			sort.Strings(headers)
		}
	}

	var rows [][]string
	for _, row := range data {
		var record []string
		switch r := row.(type) {
		case []any:
			for _, v := range r {
				record = append(record, fmt.Sprint(v))
			}
		case map[string]any:
			for _, h := range headers {
				record = append(record, fmt.Sprint(r[h]))
			}
		}
		rows = append(rows, record)
	}

	var output string
	switch format {
	case "markdown":
		output = formatMarkdownTable(headers, rows)
	case "html":
		output = formatHTMLTable(headers, rows)
	default:
		output = formatTextTable(headers, rows)
	}

	return NewResultWithMeta(output, map[string]any{"format": format, "row_count": len(rows)}), nil
}

func formatTextTable(headers []string, rows [][]string) string {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	if len(headers) > 0 {
		fmt.Fprintln(w, strings.Join(headers, "\t"))
	}
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
	return buf.String()
}

func formatMarkdownTable(headers []string, rows [][]string) string {
	var buf strings.Builder
	if len(headers) > 0 {
		buf.WriteString("| " + strings.Join(headers, " | ") + " |\n")
		separators := make([]string, len(headers))
		for i := range separators {
			separators[i] = "---"
		}
		buf.WriteString("| " + strings.Join(separators, " | ") + " |\n")
	}
	for _, row := range rows {
		buf.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	return buf.String()
}

func formatHTMLTable(headers []string, rows [][]string) string {
	var buf strings.Builder
	buf.WriteString("<table>\n")
	if len(headers) > 0 {
		buf.WriteString("  <thead><tr>")
		for _, h := range headers {
			buf.WriteString(fmt.Sprintf("<th>%s</th>", h))
		}
		buf.WriteString("</tr></thead>\n")
	}
	buf.WriteString("  <tbody>\n")
	for _, row := range rows {
		buf.WriteString("    <tr>")
		for _, cell := range row {
			buf.WriteString(fmt.Sprintf("<td>%s</td>", cell))
		}
		buf.WriteString("</tr>\n")
	}
	buf.WriteString("  </tbody>\n</table>")
	return buf.String()
}

// DiffTool compares two data structures
var DiffTool = &Tool{
	Name:        "diff",
	Description: "Compare two data structures and show differences.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"left":   {Type: "object", Description: "First value to compare"},
		"right":  {Type: "object", Description: "Second value to compare"},
		"format": EnumParam("Input format", []string{"text", "json", "auto"}),
	}, []string{"left", "right"}),
	Execute: executeDiff,
}

func executeDiff(ctx context.Context, params map[string]any) (Result, error) {
	left := params["left"]
	right := params["right"]

	if left == nil || right == nil {
		return ErrorResultFromError(fmt.Errorf("left and right are required")), nil
	}

	differences := diffJSON(left, right, "")
	equal := len(differences) == 0

	return NewResultWithMeta(map[string]any{
		"equal":       equal,
		"differences": differences,
	}, map[string]any{"diff_count": len(differences)}), nil
}

func diffJSON(left, right any, path string) []map[string]any {
	var differences []map[string]any

	if left == nil && right == nil {
		return differences
	}
	if left == nil || right == nil {
		return append(differences, map[string]any{"path": path, "left": left, "right": right, "type": "changed"})
	}

	if reflect.TypeOf(left) != reflect.TypeOf(right) {
		return append(differences, map[string]any{"path": path, "type": "type_mismatch"})
	}

	switch l := left.(type) {
	case map[string]any:
		r := right.(map[string]any)
		allKeys := make(map[string]bool)
		for k := range l {
			allKeys[k] = true
		}
		for k := range r {
			allKeys[k] = true
		}
		for k := range allKeys {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			lv, lOk := l[k]
			rv, rOk := r[k]
			if !lOk {
				differences = append(differences, map[string]any{"path": childPath, "right": rv, "type": "added"})
			} else if !rOk {
				differences = append(differences, map[string]any{"path": childPath, "left": lv, "type": "removed"})
			} else {
				differences = append(differences, diffJSON(lv, rv, childPath)...)
			}
		}
	case []any:
		r := right.([]any)
		maxLen := len(l)
		if len(r) > maxLen {
			maxLen = len(r)
		}
		for i := 0; i < maxLen; i++ {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if i >= len(l) {
				differences = append(differences, map[string]any{"path": childPath, "right": r[i], "type": "added"})
			} else if i >= len(r) {
				differences = append(differences, map[string]any{"path": childPath, "left": l[i], "type": "removed"})
			} else {
				differences = append(differences, diffJSON(l[i], r[i], childPath)...)
			}
		}
	default:
		if !reflect.DeepEqual(left, right) {
			differences = append(differences, map[string]any{"path": path, "left": left, "right": right, "type": "changed"})
		}
	}
	return differences
}

func init() {
	mustRegister(CSVTool)
	mustRegister(JSONTool)
	mustRegister(XMLTool)
	mustRegister(TableTool)
	mustRegister(DiffTool)
}

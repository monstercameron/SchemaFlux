package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// RegexTool performs pattern matching and text extraction.
var RegexTool = &Tool{
	Name:        "regex",
	Description: "Match, extract, or replace text using regular expressions",
	Category:    CategoryComputation,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action":  EnumParam("Action to perform", []string{"match", "find", "findall", "replace", "split"}),
		"pattern": StringParam("Regular expression pattern"),
		"text":    StringParam("Text to process"),
		"replace": StringParam("Replacement string (for replace action)"),
	}, []string{"action", "pattern", "text"}),
	Execute: executeRegex,
}

func executeRegex(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	pattern, _ := params["pattern"].(string)
	text, _ := params["text"].(string)
	replacement, _ := params["replace"].(string)

	if pattern == "" {
		return ErrorResult(fmt.Errorf("pattern is required")), nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return ErrorResult(fmt.Errorf("invalid pattern: %w", err)), nil
	}

	var result any
	switch action {
	case "match":
		result = re.MatchString(text)
	case "find":
		result = re.FindString(text)
	case "findall":
		result = re.FindAllString(text, -1)
	case "replace":
		result = re.ReplaceAllString(text, replacement)
	case "split":
		result = re.Split(text, -1)
	default:
		return ErrorResult(fmt.Errorf("unknown action: %s", action)), nil
	}

	return NewResultWithMeta(result, map[string]any{
		"pattern": pattern,
		"action":  action,
	}), nil
}

// RegexMatch checks if text matches a pattern.
func RegexMatch(pattern, text string) (bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(text), nil
}

// RegexFind finds the first match in text.
func RegexFind(pattern, text string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	return re.FindString(text), nil
}

// RegexFindAll finds all matches in text.
func RegexFindAll(pattern, text string) ([]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return re.FindAllString(text, -1), nil
}

// RegexReplace replaces matches with replacement.
func RegexReplace(pattern, text, replacement string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	return re.ReplaceAllString(text, replacement), nil
}

// RegexExtract extracts named groups from text.
func RegexExtract(pattern, text string) (map[string]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	match := re.FindStringSubmatch(text)
	if match == nil {
		return nil, nil
	}

	result := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if i > 0 && name != "" && i < len(match) {
			result[name] = match[i]
		}
	}

	// Also add numbered groups
	for i := 1; i < len(match); i++ {
		result[fmt.Sprintf("%d", i)] = match[i]
	}

	return result, nil
}

// ConvertTool handles unit conversions.
var ConvertTool = &Tool{
	Name:        "convert",
	Description: "Convert between units (length, weight, temperature, time, data size)",
	Category:    CategoryComputation,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"value":    NumberParam("Value to convert"),
		"from":     StringParam("Source unit"),
		"to":       StringParam("Target unit"),
		"category": EnumParam("Conversion category", []string{"length", "weight", "temperature", "time", "data"}),
	}, []string{"value", "from", "to"}),
	Execute: executeConvert,
}

// Conversion tables
var lengthConversions = map[string]float64{
	"mm": 0.001, "cm": 0.01, "m": 1, "km": 1000,
	"in": 0.0254, "ft": 0.3048, "yd": 0.9144, "mi": 1609.344,
	"inch": 0.0254, "foot": 0.3048, "feet": 0.3048, "yard": 0.9144, "mile": 1609.344,
}

var weightConversions = map[string]float64{
	"mg": 0.001, "g": 1, "kg": 1000, "t": 1000000,
	"oz": 28.3495, "lb": 453.592, "st": 6350.29,
	"ounce": 28.3495, "pound": 453.592, "stone": 6350.29,
}

var timeConversions = map[string]float64{
	"ms": 0.001, "s": 1, "sec": 1, "min": 60, "h": 3600, "hr": 3600, "hour": 3600,
	"d": 86400, "day": 86400, "w": 604800, "week": 604800,
	"mo": 2592000, "month": 2592000, "y": 31536000, "yr": 31536000, "year": 31536000,
}

var dataConversions = map[string]float64{
	"b": 1, "byte": 1, "kb": 1024, "mb": 1024 * 1024, "gb": 1024 * 1024 * 1024,
	"tb": 1024 * 1024 * 1024 * 1024, "pb": 1024 * 1024 * 1024 * 1024 * 1024,
	"bit": 0.125, "kbit": 128, "mbit": 131072, "gbit": 134217728,
}

func executeConvert(ctx context.Context, params map[string]any) (Result, error) {
	value, ok := params["value"].(float64)
	if !ok {
		// Try int
		if intVal, ok := params["value"].(int); ok {
			value = float64(intVal)
		} else {
			return ErrorResult(fmt.Errorf("value must be a number")), nil
		}
	}
	from, _ := params["from"].(string)
	to, _ := params["to"].(string)

	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))

	// Try each conversion category
	result, err := tryConvert(value, from, to)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResultWithMeta(result, map[string]any{
		"from":  from,
		"to":    to,
		"value": value,
	}), nil
}

func tryConvert(value float64, from, to string) (float64, error) {
	// Temperature is special
	if isTemperature(from) && isTemperature(to) {
		return convertTemperature(value, from, to)
	}

	// Try each conversion table
	tables := []map[string]float64{lengthConversions, weightConversions, timeConversions, dataConversions}
	for _, table := range tables {
		fromFactor, fromOk := table[from]
		toFactor, toOk := table[to]
		if fromOk && toOk {
			// Convert to base unit, then to target
			return value * fromFactor / toFactor, nil
		}
	}

	return 0, fmt.Errorf("cannot convert from %s to %s", from, to)
}

func isTemperature(unit string) bool {
	unit = strings.ToLower(unit)
	return unit == "c" || unit == "f" || unit == "k" ||
		unit == "celsius" || unit == "fahrenheit" || unit == "kelvin"
}

func convertTemperature(value float64, from, to string) (float64, error) {
	from = strings.ToLower(from)
	to = strings.ToLower(to)

	// Normalize to single letter
	if from == "celsius" {
		from = "c"
	} else if from == "fahrenheit" {
		from = "f"
	} else if from == "kelvin" {
		from = "k"
	}
	if to == "celsius" {
		to = "c"
	} else if to == "fahrenheit" {
		to = "f"
	} else if to == "kelvin" {
		to = "k"
	}

	// Convert to Celsius first
	var celsius float64
	switch from {
	case "c":
		celsius = value
	case "f":
		celsius = (value - 32) * 5 / 9
	case "k":
		celsius = value - 273.15
	default:
		return 0, fmt.Errorf("unknown temperature unit: %s", from)
	}

	// Convert from Celsius to target
	switch to {
	case "c":
		return celsius, nil
	case "f":
		return celsius*9/5 + 32, nil
	case "k":
		return celsius + 273.15, nil
	default:
		return 0, fmt.Errorf("unknown temperature unit: %s", to)
	}
}

// Convert is a convenience function for unit conversion.
func Convert(value float64, from, to string) (float64, error) {
	return tryConvert(value, strings.ToLower(from), strings.ToLower(to))
}

func init() {
	mustRegister(RegexTool)
	mustRegister(ConvertTool)
}

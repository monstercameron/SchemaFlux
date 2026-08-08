package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NowTool returns current time information.
var NowTool = &Tool{
	Name:        "now",
	Description: "Get current date/time with timezone support",
	Category:    CategoryTime,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"timezone": StringParam("Timezone (e.g., 'America/New_York', 'UTC', 'Local')"),
		"format":   StringParam("Output format (RFC3339, Unix, Date, Time, DateTime, Custom)"),
		"custom":   StringParam("Custom format string (Go time format)"),
	}, []string{}),
	Execute: executeNow,
}

func executeNow(ctx context.Context, params map[string]any) (Result, error) {
	tz, _ := params["timezone"].(string)
	format, _ := params["format"].(string)
	custom, _ := params["custom"].(string)

	var loc *time.Location
	var err error

	if tz == "" || tz == "Local" {
		loc = time.Local
	} else if tz == "UTC" {
		loc = time.UTC
	} else {
		loc, err = time.LoadLocation(tz)
		if err != nil {
			return ErrorResultFromError(fmt.Errorf("invalid timezone: %s", tz)), nil
		}
	}

	now := time.Now().In(loc)

	var formatted string
	switch strings.ToLower(format) {
	case "unix":
		formatted = fmt.Sprintf("%d", now.Unix())
	case "unixmilli":
		formatted = fmt.Sprintf("%d", now.UnixMilli())
	case "date":
		formatted = now.Format("2006-01-02")
	case "time":
		formatted = now.Format("15:04:05")
	case "datetime":
		formatted = now.Format("2006-01-02 15:04:05")
	case "custom":
		if custom == "" {
			custom = time.RFC3339
		}
		formatted = now.Format(custom)
	default:
		formatted = now.Format(time.RFC3339)
	}

	return NewResultWithMeta(formatted, map[string]any{
		"unix":     now.Unix(),
		"timezone": loc.String(),
		"weekday":  now.Weekday().String(),
		"year":     now.Year(),
		"month":    int(now.Month()),
		"day":      now.Day(),
		"hour":     now.Hour(),
		"minute":   now.Minute(),
		"second":   now.Second(),
		"is_dst":   isDST(now),
	}), nil
}

func isDST(t time.Time) bool {
	jan := time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	jul := time.Date(t.Year(), 7, 1, 0, 0, 0, 0, t.Location())
	_, janOffset := jan.Zone()
	_, julOffset := jul.Zone()
	_, nowOffset := t.Zone()

	if janOffset == julOffset {
		return false
	}
	if janOffset > julOffset {
		return nowOffset == janOffset
	}
	return nowOffset == julOffset
}

// ParseTimeTool parses time strings.
var ParseTimeTool = &Tool{
	Name:        "parse_time",
	Description: "Parse a time string into components",
	Category:    CategoryTime,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"time":     StringParam("Time string to parse"),
		"format":   StringParam("Expected format (RFC3339, Unix, Custom)"),
		"custom":   StringParam("Custom format string"),
		"timezone": StringParam("Timezone for the result"),
	}, []string{"time"}),
	Execute: executeParseTime,
}

func executeParseTime(ctx context.Context, params map[string]any) (Result, error) {
	timeStr, _ := params["time"].(string)
	format, _ := params["format"].(string)
	custom, _ := params["custom"].(string)
	tz, _ := params["timezone"].(string)

	var t time.Time
	var err error

	// Try multiple formats
	formats := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
		"01/02/2006",
		"02-Jan-2006",
		time.RFC1123,
		time.RFC822,
	}

	if format == "unix" {
		var unix int64
		_, err = fmt.Sscanf(timeStr, "%d", &unix)
		if err == nil {
			t = time.Unix(unix, 0)
		}
	} else if format == "custom" && custom != "" {
		t, err = time.Parse(custom, timeStr)
	} else {
		for _, f := range formats {
			t, err = time.Parse(f, timeStr)
			if err == nil {
				break
			}
		}
	}

	if err != nil {
		return ErrorResultFromError(fmt.Errorf("could not parse time: %s", timeStr)), nil
	}

	// Convert to requested timezone
	if tz != "" && tz != "Local" {
		loc, err := time.LoadLocation(tz)
		if err == nil {
			t = t.In(loc)
		}
	}

	return NewResult(map[string]any{
		"unix":     t.Unix(),
		"rfc3339":  t.Format(time.RFC3339),
		"date":     t.Format("2006-01-02"),
		"time":     t.Format("15:04:05"),
		"weekday":  t.Weekday().String(),
		"year":     t.Year(),
		"month":    int(t.Month()),
		"day":      t.Day(),
		"hour":     t.Hour(),
		"minute":   t.Minute(),
		"second":   t.Second(),
		"timezone": t.Location().String(),
	}), nil
}

// DurationTool calculates time differences.
var DurationTool = &Tool{
	Name:        "duration",
	Description: "Calculate duration between times or add/subtract duration",
	Category:    CategoryTime,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action", []string{"between", "add", "subtract"}),
		"from":   StringParam("Start time (RFC3339 or unix)"),
		"to":     StringParam("End time (RFC3339 or unix)"),
		"amount": StringParam("Duration to add/subtract (e.g., '1h30m', '24h', '7d')"),
	}, []string{"action"}),
	Execute: executeDuration,
}

func executeDuration(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	fromStr, _ := params["from"].(string)
	toStr, _ := params["to"].(string)
	amountStr, _ := params["amount"].(string)

	parseTime := func(s string) (time.Time, error) {
		// Try RFC3339 first
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			return t, nil
		}
		// Try unix timestamp
		var unix int64
		_, err = fmt.Sscanf(s, "%d", &unix)
		if err == nil {
			return time.Unix(unix, 0), nil
		}
		return time.Time{}, fmt.Errorf("cannot parse time: %s", s)
	}

	switch action {
	case "between":
		from, err := parseTime(fromStr)
		if err != nil {
			return ErrorResult(err), nil
		}
		to, err := parseTime(toStr)
		if err != nil {
			return ErrorResult(err), nil
		}

		duration := to.Sub(from)
		return NewResult(map[string]any{
			"seconds":      duration.Seconds(),
			"minutes":      duration.Minutes(),
			"hours":        duration.Hours(),
			"days":         duration.Hours() / 24,
			"human":        formatDuration(duration),
			"milliseconds": duration.Milliseconds(),
		}), nil

	case "add", "subtract":
		from, err := parseTime(fromStr)
		if err != nil {
			return ErrorResult(err), nil
		}

		duration, err := parseDuration(amountStr)
		if err != nil {
			return ErrorResult(err), nil
		}

		var result time.Time
		if action == "add" {
			result = from.Add(duration)
		} else {
			result = from.Add(-duration)
		}

		return NewResult(map[string]any{
			"unix":    result.Unix(),
			"rfc3339": result.Format(time.RFC3339),
			"date":    result.Format("2006-01-02"),
			"time":    result.Format("15:04:05"),
		}), nil

	default:
		return ErrorResultFromError(fmt.Errorf("unknown action: %s", action)), nil
	}
}

func parseDuration(s string) (time.Duration, error) {
	// Handle "d" for days
	if strings.HasSuffix(s, "d") {
		var days int
		_, err := fmt.Sscanf(s, "%dd", &days)
		if err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		return "-" + formatDuration(-d)
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}

	return strings.Join(parts, " ")
}

// ScheduleTool works with schedules and cron expressions.
var ScheduleTool = &Tool{
	Name:        "schedule",
	Description: "Parse natural language schedules and calculate next occurrences",
	Category:    CategoryTime,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"query":    StringParam("Natural language schedule (e.g., 'next Monday', 'in 2 hours')"),
		"from":     StringParam("Reference time (default: now)"),
		"timezone": StringParam("Timezone"),
	}, []string{"query"}),
	Execute: executeSchedule,
}

func executeSchedule(ctx context.Context, params map[string]any) (Result, error) {
	query, _ := params["query"].(string)
	fromStr, _ := params["from"].(string)
	tz, _ := params["timezone"].(string)

	var from time.Time
	if fromStr != "" {
		var err error
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			from = time.Now()
		}
	} else {
		from = time.Now()
	}

	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err == nil {
			from = from.In(loc)
		}
	}

	// Parse natural language schedule
	result, err := parseNaturalSchedule(query, from)
	if err != nil {
		return ErrorResult(err), nil
	}

	return NewResult(map[string]any{
		"unix":    result.Unix(),
		"rfc3339": result.Format(time.RFC3339),
		"date":    result.Format("2006-01-02"),
		"time":    result.Format("15:04:05"),
		"weekday": result.Weekday().String(),
		"human":   formatDuration(result.Sub(from)) + " from now",
	}), nil
}

func parseNaturalSchedule(query string, from time.Time) (time.Time, error) {
	query = strings.ToLower(strings.TrimSpace(query))

	// Handle "in X hours/minutes/days"
	if strings.HasPrefix(query, "in ") {
		parts := strings.Fields(query[3:])
		if len(parts) >= 2 {
			var amount int
			fmt.Sscanf(parts[0], "%d", &amount)
			unit := parts[1]

			switch {
			case strings.HasPrefix(unit, "second"):
				return from.Add(time.Duration(amount) * time.Second), nil
			case strings.HasPrefix(unit, "minute"):
				return from.Add(time.Duration(amount) * time.Minute), nil
			case strings.HasPrefix(unit, "hour"):
				return from.Add(time.Duration(amount) * time.Hour), nil
			case strings.HasPrefix(unit, "day"):
				return from.AddDate(0, 0, amount), nil
			case strings.HasPrefix(unit, "week"):
				return from.AddDate(0, 0, amount*7), nil
			case strings.HasPrefix(unit, "month"):
				return from.AddDate(0, amount, 0), nil
			case strings.HasPrefix(unit, "year"):
				return from.AddDate(amount, 0, 0), nil
			}
		}
	}

	// Handle "tomorrow"
	if query == "tomorrow" {
		return from.AddDate(0, 0, 1), nil
	}

	// Handle "next Monday", "next Tuesday", etc.
	if strings.HasPrefix(query, "next ") {
		dayName := strings.TrimPrefix(query, "next ")
		targetDay := parseWeekday(dayName)
		if targetDay >= 0 {
			daysUntil := (int(targetDay) - int(from.Weekday()) + 7) % 7
			if daysUntil == 0 {
				daysUntil = 7
			}
			return from.AddDate(0, 0, daysUntil), nil
		}
	}

	// Handle specific day names
	targetDay := parseWeekday(query)
	if targetDay >= 0 {
		daysUntil := (int(targetDay) - int(from.Weekday()) + 7) % 7
		if daysUntil == 0 {
			daysUntil = 7
		}
		return from.AddDate(0, 0, daysUntil), nil
	}

	return time.Time{}, fmt.Errorf("could not parse schedule: %s", query)
}

func parseWeekday(s string) time.Weekday {
	s = strings.ToLower(s)
	days := map[string]time.Weekday{
		"sunday": time.Sunday, "sun": time.Sunday,
		"monday": time.Monday, "mon": time.Monday,
		"tuesday": time.Tuesday, "tue": time.Tuesday,
		"wednesday": time.Wednesday, "wed": time.Wednesday,
		"thursday": time.Thursday, "thu": time.Thursday,
		"friday": time.Friday, "fri": time.Friday,
		"saturday": time.Saturday, "sat": time.Saturday,
	}
	if day, ok := days[s]; ok {
		return day
	}
	return -1
}

// HolidayTool checks for holidays.
var HolidayTool = &Tool{
	Name:        "holiday",
	Description: "Check if a date is a holiday or business day",
	Category:    CategoryTime,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"date":    StringParam("Date to check (YYYY-MM-DD)"),
		"country": StringParam("Country code (US, UK, etc.)"),
	}, []string{"date"}),
	Execute: executeHoliday,
}

// Simple US holiday list
var usHolidays = map[string]string{
	"01-01": "New Year's Day",
	"07-04": "Independence Day",
	"12-25": "Christmas Day",
	"12-31": "New Year's Eve",
}

func executeHoliday(ctx context.Context, params map[string]any) (Result, error) {
	dateStr, _ := params["date"].(string)
	country, _ := params["country"].(string)

	if country == "" {
		country = "US"
	}

	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return ErrorResultFromError(fmt.Errorf("invalid date format: %s", dateStr)), nil
	}

	monthDay := t.Format("01-02")
	isWeekend := t.Weekday() == time.Saturday || t.Weekday() == time.Sunday

	var holiday string
	var isHoliday bool
	if country == "US" {
		holiday, isHoliday = usHolidays[monthDay]
	}

	return NewResult(map[string]any{
		"date":        dateStr,
		"weekday":     t.Weekday().String(),
		"is_weekend":  isWeekend,
		"is_holiday":  isHoliday,
		"holiday":     holiday,
		"is_business": !isWeekend && !isHoliday,
		"country":     country,
	}), nil
}

func init() {
	mustRegister(NowTool)
	mustRegister(ParseTimeTool)
	mustRegister(DurationTool)
	mustRegister(ScheduleTool)
	mustRegister(HolidayTool)
}

package schemafluxtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The file access of this package is the os package's: Save and Replay read and
// write a directory. Under GOOS=js there is no file system to reach (the os
// package reports an error for every call, and there is none to read in a
// browser), and under GOOS=wasip1 only the directories the host gave the
// module. Both compile; on them Save and Replay return an error that names the
// directory and wraps the error of the os package, and never panic. What works
// everywhere is ReplayFrom and Tape.Recorded, which touch no file.

const (
	opSave   = "schemafluxtest.save"
	opReplay = "schemafluxtest.replay"
)

// fileError is the error of a cassette file or directory that cannot be used. It
// is of kind ErrConfiguration, since the fixture or the place it is kept is a
// part of the test's own set-up, and it wraps cause so that errors.Is(err,
// fs.ErrNotExist) and the like still work. Its message names the file and
// quotes nothing from it.
func fileError(op string, cause error, format string, args ...any) error {
	return &schemaflux.Error{
		Kind:    schemaflux.ErrConfiguration,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
		Err:     cause,
	}
}

// Save writes every exchange recorded so far into the directory Record was
// given, one file each, named 000.json, 001.json and so on in the order they
// were recorded, replacing credential shapes in what it writes (see Cassette
// for what a file holds). Writing the same exchanges again writes the same
// bytes.
//
// The directory ends up holding exactly the exchanges recorded here: numbered
// .json files left by an earlier recording are removed, because Replay plays
// every one it finds and a leftover would be played as part of this run. Files
// that are not numbered cassettes are left alone. Nothing is removed or written
// until every exchange has been encoded and checked, so a refusal leaves the
// earlier recording as it was; a Tape that recorded nothing writes nothing.
//
// Save returns an error of kind schemaflux.ErrConfiguration, naming the file or
// the exchange, when the directory cannot be made or written, when an exchange
// holds a number JSON cannot write, or when a file would still hold something
// shaped like a credential after its replacement.
func (t *Tape) Save() error {
	recorded := t.Recorded()
	if len(recorded) == 0 {
		return nil
	}
	if t.dir == "" {
		return fileError(opSave, nil, "Record was given no directory to save to")
	}

	files := make([][]byte, len(recorded))
	for i, c := range recorded {
		raw, err := redactCassette(c).MarshalJSON()
		if err != nil {
			return fileError(opSave, err, "exchange %d cannot be written as JSON (a number that is not finite?)", i+1)
		}
		var indented bytes.Buffer
		if err := json.Indent(&indented, raw, "", "  "); err != nil {
			return fileError(opSave, err, "exchange %d cannot be written as JSON", i+1)
		}
		indented.WriteByte('\n')
		if leak := findCredential(indented.String()); leak != "" {
			return fileError(opSave, nil,
				"refusing to write exchange %d: it still holds something shaped like a %s after the replacement, "+
					"which is a gap in the patterns of this package and is worth a report", i+1, leak)
		}
		files[i] = indented.Bytes()
	}

	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return fileError(opSave, err, "cannot make the directory %s", t.dir)
	}
	written := make(map[string]bool, len(files))
	for i, data := range files {
		name := fmt.Sprintf("%03d.json", i)
		if err := os.WriteFile(filepath.Join(t.dir, name), data, 0o644); err != nil {
			return fileError(opSave, err, "cannot write %s", filepath.Join(t.dir, name))
		}
		written[name] = true
	}

	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return fileError(opSave, err, "cannot read the directory %s", t.dir)
	}
	for _, entry := range entries {
		if _, numbered := cassetteNumber(entry.Name()); entry.IsDir() || !numbered || written[entry.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(t.dir, entry.Name())); err != nil {
			return fileError(opSave, err, "cannot remove the earlier cassette %s", filepath.Join(t.dir, entry.Name()))
		}
	}
	return nil
}

// Replay loads every .json file of dir as one exchange, in the order Save
// numbered them: by the value of the number in the file name, so that 1000.json
// follows 999.json, and then by name for files that are not numbered. Files that
// are not .json and directories are ignored.
//
// It returns an error of kind schemaflux.ErrConfiguration, naming the file, for
// a directory that cannot be read or holds no cassette, and for a file that is
// not valid JSON, is cut short, is of another version, holds a member or a kind
// this version does not know, or records both or neither of a response and a
// failure. It never panics, whatever the files hold, and the error says what is
// wrong with a file and never quotes it.
func Replay(dir string) (*Player, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fileError(opReplay, err, "cannot read the directory %s", dir)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil, fileError(opReplay, nil, "there are no cassettes (.json files) in %s", dir)
	}
	slices.SortFunc(names, func(a, b string) int {
		switch {
		case cassetteFileLess(a, b):
			return -1
		case cassetteFileLess(b, a):
			return 1
		}
		return 0
	})

	cassettes := make([]Cassette, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fileError(opReplay, err, "cannot read the cassette %s", path)
		}
		c, err := parseCassette(data)
		if err != nil {
			return nil, fileError(opReplay, err, "the cassette %s cannot be used: %v", path, err)
		}
		if (c.Response == nil) == (c.Failure == nil) {
			return nil, fileError(opReplay, nil, "the cassette %s records neither a response nor a failure, or both", path)
		}
		cassettes = append(cassettes, c)
	}
	return ReplayFrom(cassettes...), nil
}

// cassetteNumber returns the digits of a numbered cassette file name such as
// "007.json", and false for any other name.
func cassetteNumber(name string) (string, bool) {
	stem, ok := strings.CutSuffix(name, ".json")
	if !ok || stem == "" {
		return "", false
	}
	for _, r := range stem {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return stem, true
}

// cassetteFileLess orders cassette files for replay. Numbered files go by the
// value of their number, longer meaning larger once the leading zeros are gone,
// so that no width of number overflows and "1000.json" follows "999.json" where
// a sort of the names would put it before "101.json". Names that are not numbers
// come after the numbered ones, in the order of the names.
func cassetteFileLess(a, b string) bool {
	numberA, numberedA := cassetteNumber(a)
	numberB, numberedB := cassetteNumber(b)
	switch {
	case numberedA && numberedB:
		valueA, valueB := strings.TrimLeft(numberA, "0"), strings.TrimLeft(numberB, "0")
		if len(valueA) != len(valueB) {
			return len(valueA) < len(valueB)
		}
		if valueA != valueB {
			return valueA < valueB
		}
		return a < b
	case numberedA:
		return true
	case numberedB:
		return false
	}
	return a < b
}

// credentialPatterns are the shapes that must not reach a committed file. They
// are the shapes scripts/secret_scan.py looks for over the whole repository,
// applied one step earlier, before the file exists. The order matters twice: an
// Anthropic key also fits the OpenAI pattern, so it comes first to be named
// correctly, and a private key is matched as a whole block before the
// header-only form, which is the fallback for a block that was cut off and has no
// end line.
var credentialPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"Anthropic key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"OpenAI key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`)},
	{"AWS access key ID", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"bearer header", regexp.MustCompile(`(?i)authorization"?\s*[:=]\s*"?bearer\s+\S+`)},
	{"private key block", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----.*?-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----`)},
	{"private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----(?:\r?\n[A-Za-z0-9+/=]{8,})*`)},
}

// redactedMarker replaces a credential in a cassette and in the request a
// Player compares against one.
const redactedMarker = "[redacted by schemafluxtest]"

// redactCredentials replaces every credential shape in text with the marker.
func redactCredentials(text string) string {
	for _, entry := range credentialPatterns {
		text = entry.pattern.ReplaceAllString(text, redactedMarker)
	}
	return text
}

// findCredential names the first credential shape still present, or "".
func findCredential(text string) string {
	for _, entry := range credentialPatterns {
		if entry.pattern.MatchString(text) {
			return entry.name
		}
	}
	return ""
}

// redactCassette is c with credential shapes replaced in every text a provider
// or a caller could have put one in: the prompts, the decision, the answer and
// the tool calls. It shares nothing with c.
func redactCassette(c Cassette) Cassette {
	c = cloneCassette(c)
	c.Request = matchKey(c.Request)
	if r := c.Response; r != nil {
		r.Content = redactCredentials(r.Content)
		for i := range r.Answers {
			r.Answers[i].Label = redactCredentials(r.Answers[i].Label)
		}
		for i := range r.ToolCalls {
			call := &r.ToolCalls[i]
			call.ID, call.Name, call.Arguments = redactCredentials(call.ID), redactCredentials(call.Name), redactCredentials(call.Arguments)
		}
	}
	return c
}

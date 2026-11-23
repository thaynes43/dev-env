// Command session-retire-dry-run classifies sanitized read-only session records
// from stdin. The command does not contact services or operate on a session.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	kjson "sigs.k8s.io/json"
)

const (
	maxInput   = 2 << 20
	maxRecords = 2000
)

var sessionName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,251}[a-z0-9])?$`)

func main() {
	if len(os.Args) != 1 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: session-retire-dry-run < sanitized-records.json")
		os.Exit(2)
	}
	if err := run(os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	body, err := io.ReadAll(io.LimitReader(input, maxInput+1))
	if err != nil || len(body) > maxInput {
		return errors.New("inventory input unreadable or over 2 MiB")
	}
	var records []sessionRecord
	strict, err := kjson.UnmarshalStrict(body, &records)
	if err != nil || len(strict) > 0 || records == nil || len(records) > maxRecords {
		return errors.New("expected at most 2000 valid sanitized session records")
	}
	seen := make(map[string]bool, len(records))
	results := make([]classification, 0, len(records))
	for _, record := range records {
		if !sessionName.MatchString(record.Session) || seen[record.Session] {
			return errors.New("invalid or duplicate session name")
		}
		seen[record.Session] = true
		results = append(results, classify(record))
	}
	return json.NewEncoder(output).Encode(results)
}

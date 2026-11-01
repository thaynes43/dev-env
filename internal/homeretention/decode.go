package homeretention

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// DecodeSnapshot rejects unknown fields, duplicate JSON member names, trailing
// documents, excessive depth, oversized input, and duplicate record UIDs.
func DecodeSnapshot(r io.Reader) (Snapshot, error) {
	var snapshot Snapshot
	if err := decodeStrict(r, MaxSnapshotBytes, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func DecodeTrust(r io.Reader) (*TrustStore, error) {
	var config TrustConfig
	if err := decodeStrict(r, MaxTrustBytes, &config); err != nil {
		return nil, err
	}
	return NewTrustStore(config)
}

func decodeStrict(r io.Reader, limit int64, target any) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if int64(len(data)) > limit {
		return errors.New("JSON exceeds byte limit")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("expected exactly one JSON document")
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON schema: %w", err)
	}
	// encoding/json otherwise accepts case-insensitive field aliases. Require
	// the exact versioned JSON names as well as rejecting unknown fields.
	if err := exactFields(data, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	return nil
}

func exactFields(data []byte, schema reflect.Type) error {
	for schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	switch schema.Kind() {
	case reflect.Struct:
		var members map[string]json.RawMessage
		if err := json.Unmarshal(data, &members); err != nil {
			return err
		}
		fields := map[string]reflect.Type{}
		var collect func(reflect.Type)
		collect = func(typ reflect.Type) {
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if field.Anonymous && name == "" {
					collect(field.Type)
					continue
				}
				if name != "" && name != "-" {
					fields[name] = field.Type
				}
			}
		}
		collect(schema)
		for name, value := range members {
			typ, exists := fields[name]
			if !exists {
				return errors.New("JSON field does not match exact schema name")
			}
			if err := exactFields(value, typ); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := exactFields(value, schema.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanValue(dec *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return errors.New("invalid JSON object")
			}
			name, ok := token.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid JSON member")
			}
			seen[name] = true
			if err := scanValue(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanValue(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = dec.Token()
	return err
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.Version != Version {
		return errors.New("unsupported snapshot version")
	}
	if snapshot.Records == nil || len(snapshot.Records) > MaxRecords {
		return errors.New("records must be an explicit array within record limit")
	}
	seen := map[string]bool{}
	for _, record := range snapshot.Records {
		if !validText(record.UID) || seen[record.UID] {
			return errors.New("record UID missing, invalid, or duplicated")
		}
		seen[record.UID] = true
	}
	return nil
}

package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// PreferencesVersion is the only preference document version Phase A accepts.
	PreferencesVersion = 1
	// MaxAlertPreferences bounds AlertPreference values in one document.
	MaxAlertPreferences = 200
	// MaxTypePreferences bounds TypePreference values in one document.
	MaxTypePreferences = 100
	// MaxPreferenceBytes bounds the raw preference document.
	MaxPreferenceBytes = 128 << 10
	// MaxFingerprintBytes bounds a preference fingerprint, in bytes.
	MaxFingerprintBytes = 256
	// MaxAlertNameBytes bounds a preference alert name, in bytes.
	MaxAlertNameBytes = 256
)

// Desired is the stored wish for one control. It is not applied state.
type Desired string

const (
	DesiredUnset Desired = "unset"
	DesiredOn    Desired = "on"
	DesiredOff   Desired = "off"
)

// Valid reports whether d is unset, on, or off.
func (d Desired) Valid() bool {
	switch d {
	case DesiredUnset, DesiredOn, DesiredOff:
		return true
	default:
		return false
	}
}

// AlertPreference is one fingerprint's desired escalate and mute values.
// MuteUntil is optional. A stored time is not a silence that was created.
type AlertPreference struct {
	Fingerprint string
	Escalate    Desired
	Mute        Desired
	MuteUntil   *time.Time
}

// TypePreference is one alert name's desired automatic responder value.
type TypePreference struct {
	AlertName     string
	AutoResponder Desired
}

// ListPreference is the desired list sort and filter. Neither is applied by
// AggregateAlerts, because applying a filter would change group counts.
type ListPreference struct {
	Sort   string
	Filter string
}

// Preferences is the desired control document for one principal.
// It has no applied-state field.
type Preferences struct {
	Version int
	Alerts  []AlertPreference
	Types   []TypePreference
	List    ListPreference
}

// Principal is the exact issuer and subject pair. It matches the identity
// fields of portal.Principal. This package does not import web/portal.
// The strings are opaque: an address-shaped subject is not mail, and no
// username is derived.
type Principal struct {
	Issuer  string
	Subject string
}

// Revision is an opaque compare-and-swap token. The zero value is not a
// stored generation. UnconfiguredStore returns the zero Revision.
type Revision string

var (
	// ErrNotConfigured means this process has no preference store.
	ErrNotConfigured = errors.New("pages: controls not configured")
	// ErrConflict means expected revision is not the current revision.
	ErrConflict = errors.New("pages: controls revision conflict")
)

// ControlStore loads and saves desired preferences for one principal.
// Load of a principal with no saved row returns the zero Preferences, the
// zero Revision, and a nil error. Version 0 means there is no saved
// document. Save is compare-and-swap. An empty expected revision creates a
// row only when that principal has none. A mismatch, including that empty
// revision when a row already exists, returns ErrConflict and leaves the
// stored document unchanged. A saved document is desired state, not applied
// policy. UnconfiguredStore does not use this absent-row result: it refuses
// every call with ErrNotConfigured.
type ControlStore interface {
	Load(ctx context.Context, principal Principal) (Preferences, Revision, error)
	Save(ctx context.Context, principal Principal, prefs Preferences, expected Revision) (Revision, error)
}

// UnconfiguredStore is the production store in Phase A. It has no backend.
// Load returns the zero Preferences, the zero Revision, and ErrNotConfigured.
// Save returns ErrNotConfigured. Context, principal, and document contents
// do not change that result, and nothing is written.
type UnconfiguredStore struct{}

// Load refuses the read because no store is configured.
func (UnconfiguredStore) Load(context.Context, Principal) (Preferences, Revision, error) {
	return Preferences{}, "", ErrNotConfigured
}

// Save refuses the write because no store is configured.
func (UnconfiguredStore) Save(context.Context, Principal, Preferences, Revision) (Revision, error) {
	return "", ErrNotConfigured
}

// PreferenceSorts is the sort allowlist, in canonical byte order of the
// spellings. The values are desired list order only.
func PreferenceSorts() []string {
	return []string{"alertname", "count", "earliestStart", "latestUpdate", "severity"}
}

// PreferenceFilters is the filter allowlist. all means no status narrowing.
// The other three are current Alertmanager status names. None is applied here.
func PreferenceFilters() []string {
	return []string{"active", "all", "suppressed", "unprocessed"}
}

var (
	minPreferenceTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	maxPreferenceTime = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

var (
	preferenceRootKeys  = []string{"version", "alerts", "types", "list"}
	preferenceAlertKeys = []string{"fingerprint", "escalate", "mute", "muteUntil"}
	preferenceTypeKeys  = []string{"alertName", "autoResponder"}
	preferenceListKeys  = []string{"sort", "filter"}
)

// ParsePreferences reads the strict preference schema and returns canonical
// output. The result is sorted and its times are UTC. A refusal is
// ErrInvalid and the zero Preferences; it is not an empty saved document.
func ParsePreferences(raw []byte) (Preferences, error) {
	if err := boundedPreferenceBytes(raw); err != nil {
		return Preferences{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	prefs, err := readPreferences(dec)
	if err != nil {
		return Preferences{}, err
	}
	if err := rejectTrailing(dec); err != nil {
		return Preferences{}, err
	}
	return canonicalPreferences(prefs)
}

// MarshalPreferences encodes prefs in canonical form.
// Invalid prefs return ErrInvalid and no bytes. Encoding does not escape
// HTML delimiters. The encoded bytes, including a document whose tokens
// did not need escaping, are refused when longer than MaxPreferenceBytes.
// The result has no trailing newline.
func MarshalPreferences(prefs Preferences) ([]byte, error) {
	canon, err := canonicalPreferences(prefs)
	if err != nil {
		return nil, err
	}
	wire := wirePreferences{
		Version: canon.Version,
		Alerts:  make([]wireAlert, len(canon.Alerts)),
		Types:   make([]wireType, len(canon.Types)),
		List:    wireList{Sort: canon.List.Sort, Filter: canon.List.Filter},
	}
	for i, alert := range canon.Alerts {
		item := wireAlert{Fingerprint: alert.Fingerprint, Escalate: string(alert.Escalate), Mute: string(alert.Mute)}
		if alert.MuteUntil != nil {
			text := alert.MuteUntil.Format(time.RFC3339Nano)
			item.MuteUntil = &text
		}
		wire.Alerts[i] = item
	}
	for i, item := range canon.Types {
		wire.Types[i] = wireType{AlertName: item.AlertName, AutoResponder: string(item.AutoResponder)}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(wire); err != nil {
		return nil, fmt.Errorf("%w: encode", ErrInvalid)
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if err := boundedPreferenceBytes(out); err != nil {
		return nil, err
	}
	return out, nil
}

func boundedPreferenceBytes(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxPreferenceBytes || !utf8.Valid(raw) {
		return fmt.Errorf("%w: document", ErrInvalid)
	}
	return nil
}

func canonicalPreferences(prefs Preferences) (Preferences, error) {
	if err := validatePreferences(prefs); err != nil {
		return Preferences{}, err
	}
	alerts := append([]AlertPreference(nil), prefs.Alerts...)
	for i := range alerts {
		if alerts[i].MuteUntil == nil {
			continue
		}
		stamp, err := canonMute(*alerts[i].MuteUntil)
		if err != nil {
			return Preferences{}, err
		}
		alerts[i].MuteUntil = &stamp
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].Fingerprint < alerts[j].Fingerprint })
	types := append([]TypePreference(nil), prefs.Types...)
	sort.Slice(types, func(i, j int) bool { return types[i].AlertName < types[j].AlertName })
	if alerts == nil {
		alerts = []AlertPreference{}
	}
	if types == nil {
		types = []TypePreference{}
	}
	prefs.Alerts = alerts
	prefs.Types = types
	return prefs, nil
}

func validatePreferences(prefs Preferences) error {
	if prefs.Version != PreferencesVersion {
		return fmt.Errorf("%w: version", ErrInvalid)
	}
	if len(prefs.Alerts) > MaxAlertPreferences || len(prefs.Types) > MaxTypePreferences {
		return fmt.Errorf("%w: count", ErrInvalid)
	}
	seenFingerprint := map[string]struct{}{}
	for _, alert := range prefs.Alerts {
		if !validToken(alert.Fingerprint, MaxFingerprintBytes) {
			return fmt.Errorf("%w: fingerprint", ErrInvalid)
		}
		if _, ok := seenFingerprint[alert.Fingerprint]; ok {
			return fmt.Errorf("%w: fingerprint", ErrInvalid)
		}
		seenFingerprint[alert.Fingerprint] = struct{}{}
		if !alert.Escalate.Valid() || !alert.Mute.Valid() {
			return fmt.Errorf("%w: desired", ErrInvalid)
		}
	}
	seenName := map[string]struct{}{}
	for _, item := range prefs.Types {
		if !ConfigurableAlertName(item.AlertName) {
			return fmt.Errorf("%w: alertName", ErrInvalid)
		}
		if _, ok := seenName[item.AlertName]; ok {
			return fmt.Errorf("%w: alertName", ErrInvalid)
		}
		seenName[item.AlertName] = struct{}{}
		if !item.AutoResponder.Valid() {
			return fmt.Errorf("%w: desired", ErrInvalid)
		}
	}
	if !allowed(prefs.List.Sort, PreferenceSorts()) || !allowed(prefs.List.Filter, PreferenceFilters()) {
		return fmt.Errorf("%w: list", ErrInvalid)
	}
	return nil
}

func canonMute(stamp time.Time) (time.Time, error) {
	if stamp.IsZero() {
		return time.Time{}, fmt.Errorf("%w: muteUntil", ErrInvalid)
	}
	stamp = stamp.UTC().Round(0)
	if stamp.Before(minPreferenceTime) || !stamp.Before(maxPreferenceTime) {
		return time.Time{}, fmt.Errorf("%w: muteUntil", ErrInvalid)
	}
	return stamp, nil
}

// ConfigurableAlertName reports whether the desired-preference codec can
// store this exact alert name. Snapshot labels may have a wider vocabulary.
func ConfigurableAlertName(value string) bool {
	return validToken(value, MaxAlertNameBytes)
}

func validToken(value string, max int) bool {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

func allowed(value string, choices []string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func readPreferences(dec *json.Decoder) (Preferences, error) {
	obj, err := openObject(dec, preferenceRootKeys)
	if err != nil {
		return Preferences{}, err
	}
	var prefs Preferences
	var version, alerts, types, list bool
	for {
		key, ok, err := obj.next()
		if err != nil {
			return Preferences{}, err
		}
		if !ok {
			break
		}
		switch key {
		case "version":
			prefs.Version, err = readVersion(dec)
			version = err == nil
		case "alerts":
			prefs.Alerts, err = readAlerts(dec)
			alerts = err == nil
		case "types":
			prefs.Types, err = readTypes(dec)
			types = err == nil
		case "list":
			prefs.List, err = readList(dec)
			list = err == nil
		default:
			err = fmt.Errorf("%w: unknown field", ErrInvalid)
		}
		if err != nil {
			return Preferences{}, err
		}
	}
	if !version || !alerts || !types || !list {
		return Preferences{}, fmt.Errorf("%w: missing field", ErrInvalid)
	}
	return prefs, nil
}

func readAlerts(dec *json.Decoder) ([]AlertPreference, error) {
	if err := expectDelim(dec, '['); err != nil {
		return nil, err
	}
	out := []AlertPreference{}
	for dec.More() {
		if len(out) >= MaxAlertPreferences {
			return nil, fmt.Errorf("%w: alerts", ErrInvalid)
		}
		item, err := readAlert(dec)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := expectDelim(dec, ']'); err != nil {
		return nil, err
	}
	return out, nil
}

func readAlert(dec *json.Decoder) (AlertPreference, error) {
	obj, err := openObject(dec, preferenceAlertKeys)
	if err != nil {
		return AlertPreference{}, err
	}
	var alert AlertPreference
	var fingerprint, escalate, mute bool
	for {
		key, ok, err := obj.next()
		if err != nil {
			return AlertPreference{}, err
		}
		if !ok {
			break
		}
		switch key {
		case "fingerprint":
			alert.Fingerprint, err = readString(dec)
			fingerprint = err == nil
		case "escalate":
			alert.Escalate, err = readDesired(dec)
			escalate = err == nil
		case "mute":
			alert.Mute, err = readDesired(dec)
			mute = err == nil
		case "muteUntil":
			text, readErr := readString(dec)
			if readErr != nil {
				return AlertPreference{}, readErr
			}
			stamp, parseErr := time.Parse(time.RFC3339Nano, text)
			if parseErr != nil {
				return AlertPreference{}, fmt.Errorf("%w: muteUntil", ErrInvalid)
			}
			alert.MuteUntil = &stamp
		default:
			err = fmt.Errorf("%w: unknown field", ErrInvalid)
		}
		if err != nil {
			return AlertPreference{}, err
		}
	}
	if !fingerprint || !escalate || !mute {
		return AlertPreference{}, fmt.Errorf("%w: missing field", ErrInvalid)
	}
	return alert, nil
}

func readTypes(dec *json.Decoder) ([]TypePreference, error) {
	if err := expectDelim(dec, '['); err != nil {
		return nil, err
	}
	out := []TypePreference{}
	for dec.More() {
		if len(out) >= MaxTypePreferences {
			return nil, fmt.Errorf("%w: types", ErrInvalid)
		}
		item, err := readType(dec)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := expectDelim(dec, ']'); err != nil {
		return nil, err
	}
	return out, nil
}

func readType(dec *json.Decoder) (TypePreference, error) {
	obj, err := openObject(dec, preferenceTypeKeys)
	if err != nil {
		return TypePreference{}, err
	}
	var item TypePreference
	var name, responder bool
	for {
		key, ok, err := obj.next()
		if err != nil {
			return TypePreference{}, err
		}
		if !ok {
			break
		}
		switch key {
		case "alertName":
			item.AlertName, err = readString(dec)
			name = err == nil
		case "autoResponder":
			item.AutoResponder, err = readDesired(dec)
			responder = err == nil
		default:
			err = fmt.Errorf("%w: unknown field", ErrInvalid)
		}
		if err != nil {
			return TypePreference{}, err
		}
	}
	if !name || !responder {
		return TypePreference{}, fmt.Errorf("%w: missing field", ErrInvalid)
	}
	return item, nil
}

func readList(dec *json.Decoder) (ListPreference, error) {
	obj, err := openObject(dec, preferenceListKeys)
	if err != nil {
		return ListPreference{}, err
	}
	var list ListPreference
	var sortKey, filter bool
	for {
		key, ok, err := obj.next()
		if err != nil {
			return ListPreference{}, err
		}
		if !ok {
			break
		}
		switch key {
		case "sort":
			list.Sort, err = readString(dec)
			sortKey = err == nil
		case "filter":
			list.Filter, err = readString(dec)
			filter = err == nil
		default:
			err = fmt.Errorf("%w: unknown field", ErrInvalid)
		}
		if err != nil {
			return ListPreference{}, err
		}
	}
	if !sortKey || !filter {
		return ListPreference{}, fmt.Errorf("%w: missing field", ErrInvalid)
	}
	return list, nil
}

type objectReader struct {
	dec     *json.Decoder
	allowed []string
	seen    map[string]bool
}

func openObject(dec *json.Decoder, allowed []string) (*objectReader, error) {
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	return &objectReader{dec: dec, allowed: allowed, seen: map[string]bool{}}, nil
}

func (r *objectReader) next() (string, bool, error) {
	if !r.dec.More() {
		if err := expectDelim(r.dec, '}'); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	tok, err := r.dec.Token()
	if err != nil {
		return "", false, fmt.Errorf("%w: json", ErrInvalid)
	}
	key, ok := tok.(string)
	if !ok || key == "" {
		return "", false, fmt.Errorf("%w: key", ErrInvalid)
	}
	if err := classifyKey(key, r.allowed, r.seen); err != nil {
		return "", false, err
	}
	return key, true, nil
}

func classifyKey(key string, allowed []string, seen map[string]bool) error {
	if seen[key] {
		return fmt.Errorf("%w: duplicate field", ErrInvalid)
	}
	for _, canon := range allowed {
		if key == canon {
			seen[key] = true
			return nil
		}
	}
	for _, canon := range allowed {
		if strings.EqualFold(key, canon) {
			return fmt.Errorf("%w: ambiguous field", ErrInvalid)
		}
	}
	return fmt.Errorf("%w: unknown field", ErrInvalid)
}

func readVersion(dec *json.Decoder) (int, error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, fmt.Errorf("%w: version", ErrInvalid)
	}
	number, ok := tok.(json.Number)
	if !ok || number.String() != "1" {
		return 0, fmt.Errorf("%w: version", ErrInvalid)
	}
	return PreferencesVersion, nil
}

func readDesired(dec *json.Decoder) (Desired, error) {
	text, err := readString(dec)
	if err != nil {
		return "", err
	}
	return Desired(text), nil
}

func readString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("%w: json", ErrInvalid)
	}
	if tok == nil {
		return "", fmt.Errorf("%w: null", ErrInvalid)
	}
	text, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("%w: type", ErrInvalid)
	}
	return text, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: json", ErrInvalid)
	}
	if tok == nil {
		return fmt.Errorf("%w: null", ErrInvalid)
	}
	got, ok := tok.(json.Delim)
	if !ok || got != want {
		return fmt.Errorf("%w: type", ErrInvalid)
	}
	return nil
}

func rejectTrailing(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing", ErrInvalid)
	}
	return nil
}

type wirePreferences struct {
	Version int         `json:"version"`
	Alerts  []wireAlert `json:"alerts"`
	Types   []wireType  `json:"types"`
	List    wireList    `json:"list"`
}

type wireAlert struct {
	Fingerprint string  `json:"fingerprint"`
	Escalate    string  `json:"escalate"`
	Mute        string  `json:"mute"`
	MuteUntil   *string `json:"muteUntil,omitempty"`
}

type wireType struct {
	AlertName     string `json:"alertName"`
	AutoResponder string `json:"autoResponder"`
}

type wireList struct {
	Sort   string `json:"sort"`
	Filter string `json:"filter"`
}

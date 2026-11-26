package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
)

type b3Target struct {
	Owner        string `json:"owner"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	TypeName     string `json:"type_name"`
	Generation   string `json:"generation"`
}

type b3Principal struct {
	Type     string `json:"subject_type"`
	ID       string `json:"subject_id"`
	Grant    string `json:"authorization_ref"`
	Revision string `json:"authorization_revision"`
}

type b3CommandParams struct {
	TypeName         string         `json:"type_name"`
	DocumentID       string         `json:"document_id"`
	Generation       string         `json:"generation"`
	ExpectedRevision string         `json:"expected_revision,omitempty"`
	Patch            map[string]any `json:"patch,omitempty"`
	Fields           []string       `json:"fields,omitempty"`
	SourceEventID    string         `json:"source_event_id,omitempty"`
}

type b3Cause struct {
	Root   string   `json:"root_id"`
	Budget string   `json:"root_budget_ref"`
	Parent string   `json:"parent_invocation_id,omitempty"`
	Event  string   `json:"parent_event_id,omitempty"`
	Depth  *int     `json:"depth"`
	Path   []string `json:"rule_path"`
}

type b3WireCommand struct {
	Version        string          `json:"schema_version"`
	Kind           string          `json:"kind"`
	OperationID    string          `json:"operation_id"`
	InvocationID   string          `json:"invocation_id"`
	Capability     string          `json:"capability_id"`
	Contract       string          `json:"contract_version"`
	Hash           string          `json:"canonical_input_hash"`
	DefinitionHash string          `json:"expected_definition_hash,omitempty"`
	Principal      b3Principal     `json:"principal"`
	Target         b3Target        `json:"target"`
	Params         b3CommandParams `json:"params"`
	Cause          b3Cause         `json:"causality"`
	Submitted      time.Time       `json:"submitted_at"`
	Expires        time.Time       `json:"expires_at"`
	Entry          string          `json:"entrypoint"`
}

type b3WireAdmission struct {
	Version     string    `json:"schema_version"`
	Kind        string    `json:"kind"`
	Purpose     string    `json:"purpose"`
	Audience    string    `json:"audience"`
	OperationID string    `json:"operation_id"`
	Hash        string    `json:"canonical_input_hash"`
	Grant       string    `json:"authorization_ref"`
	Revision    string    `json:"authorization_revision"`
	Target      b3Target  `json:"target"`
	Fields      []string  `json:"authorized_fields"`
	Fence       string    `json:"fence"`
	Issued      time.Time `json:"issued_at"`
	Expires     time.Time `json:"expires_at"`
}

type b3Wire struct {
	Version   string          `json:"schema_version"`
	Kind      string          `json:"kind"`
	Command   b3WireCommand   `json:"command"`
	Admission b3WireAdmission `json:"admission"`
}

func b3BadCommand() error {
	return apperr.New(400, apperr.CodeValidation, "invalid ActiveList command")
}
func b3Conflict() error {
	return apperr.New(409, apperr.CodeConflict, "ActiveList command conflicts with the original operation or generation")
}
func b3Positive(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}

func (w b3Wire) live(now time.Time) bool {
	a, c := w.Admission, w.Command
	return !a.Issued.Before(c.Submitted) && !a.Issued.After(now) && now.Before(a.Expires) && a.Expires.Sub(a.Issued) <= 30*time.Second && c.Expires.After(c.Submitted) && (w.Kind == "al_receipt_request" || !a.Expires.After(c.Expires))
}

func b3HexWord(raw []byte) (uint64, bool) {
	if len(raw) < 6 || raw[0] != '\\' || raw[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(string(raw[2:6]), 16, 16)
	return n, err == nil
}

// encoding/json normally replaces malformed UTF-16 escapes with U+FFFD.
func b3UnicodeValid(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		n, ok := b3HexWord(raw[i:])
		if !ok {
			i++
			continue
		}
		i += 5
		switch {
		case n >= 0xd800 && n <= 0xdbff:
			low, ok := b3HexWord(raw[i+1:])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		case n >= 0xdc00 && n <= 0xdfff:
			return false
		}
	}
	return true
}

// Walk the small, bounded wire tree before typed decoding to reject duplicate
// keys and case aliases. The only nullable inputs are patch values.
func b3CheckJSON(dec *json.Decoder, depth int, nullable bool, key string) error {
	if depth > 12 {
		return b3BadCommand()
	}
	token, err := dec.Token()
	if err != nil {
		return b3BadCommand()
	}
	if value, ok := token.(string); ok && value == "" && !nullable {
		return b3BadCommand()
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			name, err := dec.Token()
			k, ok := name.(string)
			if err != nil || !ok || !b3FieldName.MatchString(k) || seen[k] {
				return b3BadCommand()
			}
			seen[k] = true
			if err := b3CheckJSON(dec, depth+1, key == "patch", k); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for dec.More() {
			if err := b3CheckJSON(dec, depth+1, false, ""); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case nil:
		if !nullable {
			return b3BadCommand()
		}
	}
	return nil
}

func b3AsMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&out)
	return out
}

func b3Hash(value map[string]any) string {
	raw, _ := canonicalB3JSON(value)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func b3QualifiedFields(fields, allowed []string) bool {
	if len(fields) == 0 || len(fields) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, name := range fields {
		if seen[name] || !slices.Contains(allowed, name) {
			return false
		}
		seen[name] = true
	}
	return true
}

func (s *DataService) decodeB3Command(raw []byte, write bool, now time.Time) (b3Wire, error) {
	return s.decodeB3Request(raw, write, false, now)
}

func (s *DataService) decodeB3Lookup(raw []byte, now time.Time) (b3Wire, error) {
	return s.decodeB3Request(raw, true, true, now)
}

func (s *DataService) decodeB3Request(raw []byte, write, lookup bool, now time.Time) (b3Wire, error) {
	var w b3Wire
	if len(raw) == 0 || len(raw) > 256<<10 || !b3UnicodeValid(raw) {
		return w, b3BadCommand()
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := b3CheckJSON(dec, 0, false, ""); err != nil {
		return w, b3BadCommand()
	}
	if _, err := dec.Token(); err != io.EOF {
		return w, b3BadCommand()
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return w, b3BadCommand()
	}
	c, a := w.Command, w.Admission
	if w.Version != "1" || c.Version != "1" || c.Kind != "command" || c.Contract != "1" || a.Version != "1" || !b3Identifier.MatchString(c.OperationID) || !b3Identifier.MatchString(c.InvocationID) || !b3Identifier.MatchString(c.Principal.Grant) || !b3Positive(c.Principal.Revision) || strings.TrimSpace(c.Principal.ID) == "" || utf8.RuneCountInString(c.Principal.ID) > 128 {
		return w, b3BadCommand()
	}
	if !slices.Contains([]string{"ticket", "http", "internal", "schedule", "subscription"}, c.Entry) || c.Submitted.IsZero() || c.Expires.IsZero() || !b3Identifier.MatchString(c.Cause.Root) || !b3Identifier.MatchString(c.Cause.Budget) || c.Cause.Depth == nil || *c.Cause.Depth < 0 || *c.Cause.Depth > 8 || c.Cause.Path == nil || len(c.Cause.Path) > 8 {
		return w, b3BadCommand()
	}
	seen := map[string]bool{}
	for _, rule := range c.Cause.Path {
		if !b3Identifier.MatchString(rule) || seen[rule] {
			return w, b3BadCommand()
		}
		seen[rule] = true
	}
	for _, ref := range []string{c.Cause.Parent, c.Cause.Event, c.Params.SourceEventID} {
		if ref != "" && !b3Identifier.MatchString(ref) {
			return w, b3BadCommand()
		}
	}
	if c.Target.Owner != "activelist" || c.Target.ResourceType != "activelist_document" || !b3Positive(c.Target.ResourceID) || !b3Positive(c.Target.Generation) || c.Target.TypeName != c.Params.TypeName || c.Target.ResourceID != c.Params.DocumentID || c.Target.Generation != c.Params.Generation {
		return w, b3BadCommand()
	}
	if c.Target.TypeName != s.b3.TypeName || s.b3.TypeName == "" {
		return w, b3Forbidden()
	}
	if c.Target.Generation != strconv.FormatInt(s.b3Generation, 10) {
		return w, b3Conflict()
	}
	grant, revision, subject, id := s.b3.ReadGrantRef, s.b3.ReadGrantRevision, "service", s.b3.ReadSubjectID
	fields, allowed := c.Params.Fields, s.b3.ReadFields
	action, kind, claim := "get", "al_read_request", "read_admission_claims"
	if write {
		action, kind, claim = "update", "al_execution_request", "admission_claims"
		grant, revision, subject, id = s.b3.GrantRef, s.b3.GrantRevision, "human", s.b3.SubjectID
		fields, allowed = slices.Sorted(maps.Keys(c.Params.Patch)), s.b3.WriteFields
		if c.Params.Fields != nil || c.Params.SourceEventID != "" || !b3Positive(c.Params.ExpectedRevision) {
			return w, b3BadCommand()
		}
		for key, value := range c.Params.Patch {
			if !validB3EventValue(value) {
				return w, b3BadCommand()
			}
			if n, ok := value.(json.Number); ok {
				i, _ := n.Int64()
				c.Params.Patch[key] = float64(i)
			}
		}
	} else if c.Params.Patch != nil || c.Params.ExpectedRevision != "" {
		return w, b3BadCommand()
	}
	purpose := "al_document_" + action
	if lookup {
		kind, claim, purpose = "al_receipt_request", "receipt_admission_claims", "al_effect_receipt"
	}
	if !w.live(now) || w.Kind != kind || a.Kind != claim || a.Purpose != purpose || a.Audience != "activelist" || c.Capability != "activelist.document."+action || c.Principal.Type != subject || id != "" && id != c.Principal.ID || c.Principal.Grant != grant || c.Principal.Revision != revision || a.Grant != grant || a.Revision != revision || a.OperationID != c.OperationID || a.Target != c.Target || !b3Positive(a.Fence) || a.Hash != c.Hash {
		return w, b3Forbidden()
	}
	if !b3QualifiedFields(fields, allowed) || !b3QualifiedFields(a.Fields, allowed) || !b3QualifiedFields(fields, a.Fields) {
		return w, b3Forbidden()
	}
	projection := map[string]any{"capability_id": c.Capability, "contract_version": c.Contract, "target": b3AsMap(c.Target), "params": b3AsMap(c.Params), "subject": map[string]any{"subject_type": c.Principal.Type, "subject_id": c.Principal.ID}}
	if c.DefinitionHash != "" {
		decoded, err := hex.DecodeString(c.DefinitionHash)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != c.DefinitionHash {
			return w, b3BadCommand()
		}
		projection["expected_definition_hash"] = c.DefinitionHash
	}
	if c.Hash != b3Hash(projection) {
		return w, b3Forbidden()
	}
	return w, nil
}

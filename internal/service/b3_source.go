package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/activelist/internal/config"
	"github.com/tracerbiubiubiu/activelist/internal/meta"
	"github.com/tracerbiubiubiu/activelist/internal/repository"
)

var b3Identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{15,127}$`)
var b3FieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

const b3PendingLimit = 1024

type b3ProducerKey struct{}

// B3ProducerContext carries the caller AK verified by the HTTP verifier.
func B3ProducerContext(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, b3ProducerKey{}, caller)
}

// EnableB3 validates and pins the adopted stream before serving requests.
func (s *DataService) EnableB3(ctx context.Context, profile config.B3) error {
	if profile.TypeName == "" {
		var commissioned bool
		if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM _al_b3_stream)").Scan(&commissioned); err != nil {
			return fmt.Errorf("check ActiveList B3 stream: %w", err)
		}
		if commissioned {
			return errors.New("commissioned ActiveList B3 stream requires its original profile")
		}
		return nil
	}
	endpoint, err := url.Parse(profile.PushURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.Path != "/internal/activelist/changes/push" ||
		endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return errors.New("ActiveList B3 push URL requires a fixed Core changes endpoint")
	}

	if !b3FieldName.MatchString(profile.TypeName) || !b3FieldName.MatchString(profile.AnchorField) ||
		profile.AnchorValue == "" || profile.ProducerAK == "" || profile.CoreAK == "" ||
		profile.ProducerAK == profile.CoreAK || profile.PushAK == "" || profile.PushSK == "" ||
		!validB3Grant(profile.GrantRef, profile.GrantRevision) ||
		!validB3Grant(profile.ReadGrantRef, profile.ReadGrantRevision) || profile.ReadGrantRef == profile.GrantRef ||
		!b3Identifier.MatchString(profile.ConsumerID) {
		return errors.New("invalid ActiveList B3 stream profile")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var generation int64
	if err := tx.QueryRow(ctx, "SELECT id FROM data_types WHERE type_name=$1 AND status='active' FOR UPDATE", profile.TypeName).Scan(&generation); err != nil {
		return fmt.Errorf("load ActiveList B3 type identity: %w", err)
	}
	def, err := meta.GetByName(ctx, tx, profile.TypeName)
	if err != nil {
		return err
	}
	if err := validateB3Fields(def, &profile); err != nil {
		return err
	}
	// Pin protocol-impacting choices. Deployment endpoints and credentials can rotate.
	fingerprint, err := json.Marshal([]any{profile.TypeName, profile.AnchorField, profile.AnchorValue,
		profile.WriteFields, profile.ReadFields, profile.EventFields, profile.ConsumerID, profile.GrantRef, profile.ReadGrantRef, profile.SubjectID, profile.ReadSubjectID})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(fingerprint)
	var pinned int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO _al_b3_stream (generation,profile_sha256) VALUES ($1,$2)
		ON CONFLICT (singleton) DO UPDATE SET profile_sha256=excluded.profile_sha256
		  WHERE _al_b3_stream.generation=excluded.generation AND _al_b3_stream.profile_sha256=excluded.profile_sha256
		RETURNING generation`, generation, hex.EncodeToString(hash[:])).Scan(&pinned); err != nil {
		return fmt.Errorf("pin ActiveList B3 stream: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.b3, s.b3Generation = profile, pinned
	return nil
}

func validB3Grant(ref, revision string) bool {
	value, err := strconv.ParseInt(revision, 10, 64)
	return b3Identifier.MatchString(ref) && err == nil && value > 0 && strconv.FormatInt(value, 10) == revision
}

// B3RestrictedCaller identifies a role-specific integration caller.
func (s *DataService) B3RestrictedCaller(caller string) bool {
	return s.b3.TypeName != "" && caller != "" && (caller == s.b3.ProducerAK || caller == s.b3.CoreAK)
}

// B3ProducerRoute permits the dedicated direct producer on its two existing writes.
func (s *DataService) B3ProducerRoute(caller, typeName, route string) bool {
	return caller == s.b3.ProducerAK && s.B3GuardsLegacy(typeName) &&
		(route == "/api/v1/data/:typeName" || route == "/api/v1/data/:typeName/:id/update")
}

func (s *DataService) b3ProducerProjection(doc *repository.Document) *repository.Document {
	projected := *doc
	projected.Data = map[string]any{s.b3.AnchorField: s.b3.AnchorValue}
	for _, name := range s.b3.WriteFields {
		if value, exists := doc.Data[name]; exists {
			projected.Data[name] = value
		}
	}
	return &projected
}

func validateB3Fields(def *meta.Definition, profile *config.B3) error {
	fields := make(map[string]meta.Field, len(def.Fields))
	for _, field := range def.Fields {
		fields[field.Name] = field
	}
	anchor := fields[profile.AnchorField]
	if anchor.Type != "string" || !anchor.Required || anchor.Sensitive {
		return errors.New("ActiveList B3 anchor must be a required ordinary string field")
	}
	for _, allowed := range []struct {
		fields *[]string
		max    int
	}{{&profile.WriteFields, 32}, {&profile.ReadFields, 32}, {&profile.EventFields, 8}} {
		if len(*allowed.fields) == 0 || len(*allowed.fields) > allowed.max {
			return errors.New("ActiveList B3 field allowlist size is invalid")
		}
		names := slices.Clone(*allowed.fields)
		slices.Sort(names)
		for i, name := range names {
			field := fields[name]
			if name == profile.AnchorField || field.Sensitive || (field.Type != "int" && field.Type != "string") ||
				(i > 0 && names[i-1] == name) {
				return errors.New("ActiveList B3 allowlist requires unique non-sensitive scalar fields")
			}
		}
		*allowed.fields = names
	}
	return nil
}

func b3Forbidden() error {
	return apperr.New(http.StatusForbidden, apperr.CodeForbidden, "ActiveList B3 profile does not allow this request")
}

// B3GuardsLegacy reports a monitored type that must use the approved write or read path.
func (s *DataService) B3GuardsLegacy(typeName string) bool {
	return s.b3.TypeName != "" && s.b3.TypeName == typeName
}

func (s *DataService) validateB3Producer(ctx context.Context, typeName string, patch map[string]any) error {
	caller, _ := ctx.Value(b3ProducerKey{}).(string)
	if !s.B3GuardsLegacy(typeName) {
		if caller != "" && (caller == s.b3.ProducerAK || caller == s.b3.CoreAK) {
			return b3Forbidden()
		}
		return nil
	}
	if caller == "" || caller != s.b3.ProducerAK || len(patch) == 0 {
		return b3Forbidden()
	}
	for name := range patch {
		if !slices.Contains(s.b3.WriteFields, name) {
			return b3Forbidden()
		}
	}
	return nil
}

// lockB3Stream is acquired before the document row in every monitored source write.
func (s *DataService) lockB3Stream(ctx context.Context, tx pgx.Tx, typeName string) (int64, error) {
	if !s.B3GuardsLegacy(typeName) {
		return 0, b3Forbidden()
	}
	var generation int64
	if err := tx.QueryRow(ctx, "SELECT generation FROM _al_b3_stream FOR UPDATE").Scan(&generation); err != nil {
		return 0, b3Dependency()
	}
	if generation != s.b3Generation {
		return 0, b3Dependency()
	}
	return generation, nil
}

func b3Dependency() error {
	return apperr.New(http.StatusServiceUnavailable, apperr.CodeDependency, "ActiveList B3 change stream unavailable")
}

func b3Changed(old, next map[string]any, allow []string) []string {
	fields := make([]string, 0, len(allow))
	for _, field := range allow {
		before, oldOK := old[field]
		after, newOK := next[field]
		if oldOK != newOK || !reflect.DeepEqual(before, after) {
			fields = append(fields, field)
		}
	}
	return fields
}

func b3RandomID(prefix string) string {
	var id [16]byte
	rand.Read(id[:])
	return prefix + hex.EncodeToString(id[:])
}

func (s *DataService) appendB3Fact(ctx context.Context, tx pgx.Tx, doc *repository.Document, typeName, eventType string, changedFields []string, operationID string, causality map[string]any) error {
	var cursor int64
	if err := tx.QueryRow(ctx, `
		UPDATE _al_b3_stream SET cursor=cursor+1 WHERE cursor-ack_cursor < $1 RETURNING cursor`, b3PendingLimit).Scan(&cursor); err != nil {
		return b3Dependency()
	}
	if operationID == "" {
		operationID = b3RandomID("al-op-")
	}
	if causality == nil {
		causality = map[string]any{
			"root_id": operationID, "root_budget_ref": b3RandomID("al-budget-"),
			"depth": 0, "rule_path": []string{},
		}
	}
	values := make(map[string]any)
	for _, name := range s.b3.EventFields {
		if value, exists := doc.Data[name]; exists {
			if !validB3EventValue(value) {
				return apperr.New(http.StatusUnprocessableEntity, apperr.CodeValidation, "ActiveList B3 projected field is outside event bounds")
			}
			if number, ok := value.(float64); ok {
				value = int64(number)
			}
			values[name] = value
		}
	}
	eventID := b3RandomID("al-event-")
	generation := strconv.FormatInt(s.b3Generation, 10)
	docID := strconv.FormatInt(doc.ID, 10)
	event := map[string]any{
		"specversion": "1.0", "id": eventID, "source": "urn:zhuzhao:activelist:" + typeName,
		"type": eventType, "subject": typeName + "/" + docID,
		"time": time.Now().UTC().Format(time.RFC3339Nano), "datacontenttype": "application/json",
		"data": map[string]any{
			"stream_id": "activelist-stream-" + typeName + "-" + generation,
			"cursor":    strconv.FormatInt(cursor, 10), "generation": generation,
			"document_id": docID, "document_revision": strconv.FormatInt(doc.Version, 10),
			"changed_fields": changedFields, "event_values": values, "operation_id": operationID, "causality": causality,
		},
	}
	payload, err := canonicalB3JSON(event)
	if err != nil {
		return b3Dependency()
	}
	sum := sha256.Sum256(payload)
	if _, err := tx.Exec(ctx, "INSERT INTO _al_b3_outbox (cursor,event_id,event,payload_sha256) VALUES ($1,$2,$3,$4)",
		cursor, eventID, payload, hex.EncodeToString(sum[:])); err != nil {
		return b3Dependency()
	}
	return nil
}

func validB3EventValue(value any) bool {
	const maxInteger = 9007199254740991
	switch v := value.(type) {
	case nil, bool:
		return true
	case string:
		return utf8.RuneCountInString(v) <= 1024
	case float64:
		return v >= -maxInteger && v <= maxInteger && v == float64(int64(v))
	case json.Number:
		i, err := v.Int64()
		return err == nil && i >= -maxInteger && i <= maxInteger && strconv.FormatInt(i, 10) == string(v)
	default:
		return false
	}
}

// canonicalB3JSON matches the repository reference's sorted compact UTF-8
// encoding for the controlled ASCII keys and safe integers. Go otherwise
// escapes the two JavaScript line separator characters.
func canonicalB3JSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	raw := strings.TrimSuffix(buf.String(), "\n")
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if strings.HasPrefix(raw[i:], `\u2028`) || strings.HasPrefix(raw[i:], `\u2029`) {
				if raw[i+5] == '8' {
					out.WriteRune('\u2028')
				} else {
					out.WriteRune('\u2029')
				}
				i += 5
				continue
			}
			out.WriteByte(raw[i])
			i++
		}
		out.WriteByte(raw[i])
	}
	return []byte(out.String()), nil
}

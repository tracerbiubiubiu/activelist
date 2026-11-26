package service

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tracerbiubiubiu/activelist/internal/config"
)

const b3PublishedUpdate = `{"schema_version":"1","kind":"al_execution_request","command":{"schema_version":"1","kind":"command","operation_id":"op-demo-0000000001","invocation_id":"inv-demo-000000001","capability_id":"activelist.document.update","contract_version":"1","canonical_input_hash":"a87db27127eee5cb2b638224ec1055108458d31e053d469874095b365baadc19","principal":{"subject_type":"human","subject_id":"2","authorization_ref":"auth-demo-000000001","authorization_revision":"7"},"target":{"owner":"activelist","resource_type":"activelist_document","resource_id":"1001","type_name":"device_status","generation":"7"},"params":{"type_name":"device_status","document_id":"1001","expected_revision":"42","generation":"7","patch":{"status":"healthy"}},"causality":{"root_id":"root-demo-000000001","root_budget_ref":"budget-demo-0000001","depth":0,"rule_path":[]},"submitted_at":"2026-11-13T10:00:00Z","expires_at":"2026-11-13T10:15:00Z","entrypoint":"schedule"},"admission":{"schema_version":"1","kind":"admission_claims","purpose":"al_document_update","audience":"activelist","operation_id":"op-demo-0000000001","canonical_input_hash":"a87db27127eee5cb2b638224ec1055108458d31e053d469874095b365baadc19","authorization_ref":"auth-demo-000000001","authorization_revision":"7","target":{"owner":"activelist","resource_type":"activelist_document","resource_id":"1001","type_name":"device_status","generation":"7"},"authorized_fields":["status"],"fence":"7","issued_at":"2026-11-13T10:00:00Z","expires_at":"2026-11-13T10:00:30Z"}}`

func vectorTime() time.Time { return time.Date(2026, 11, 13, 10, 0, 1, 0, time.UTC) }
func TestB3PublishedCommandBinding(t *testing.T) {
	s := DataService{b3Generation: 7, b3: config.B3{TypeName: "device_status", GrantRef: "auth-demo-000000001", GrantRevision: "7", WriteFields: []string{"status"}}}
	cmd, err := s.decodeB3Command([]byte(b3PublishedUpdate), true, vectorTime())
	require.NoError(t, err)
	require.Equal(t, "op-demo-0000000001", cmd.Command.OperationID)
	for _, pair := range [][2]string{{"healthy", "dirty"}, {`"authorized_fields":["status"]`, `"authorized_fields":["owner"]`}, {`"purpose":"al_document_update"`, `"purpose":"al_document_get"`}, {`"schema_version":"1"`, `"schema_version":"1","schema_version":"1"`}, {`"subject_type":"human"`, `"subject_type":"service"`}, {`"expires_at":"2026-11-13T10:00:30Z"`, `"expires_at":"2026-11-13T10:01:30Z"`}, {`"subject_id":"2"`, `"subject_id":"\ud800"`}} {
		_, err = s.decodeB3Command([]byte(strings.ReplaceAll(b3PublishedUpdate, pair[0], pair[1])), true, vectorTime())
		require.Error(t, err, pair)
	}
	_, err = s.decodeB3Command([]byte(b3PublishedUpdate), true, vectorTime().Add(time.Minute))
	require.Error(t, err)
}

func TestB3LookupAcceptsExpiredOriginalButNeverUpdateWire(t *testing.T) {
	s := DataService{b3Generation: 7, b3: config.B3{TypeName: "device_status", GrantRef: "auth-demo-000000001", GrantRevision: "7", WriteFields: []string{"status"}}}
	raw := strings.NewReplacer("al_execution_request", "al_receipt_request", "admission_claims", "receipt_admission_claims", "al_document_update", "al_effect_receipt", `"issued_at":"2026-11-13T10:00:00Z"`, `"issued_at":"2026-11-14T10:00:00Z"`, `"expires_at":"2026-11-13T10:00:30Z"`, `"expires_at":"2026-11-14T10:00:30Z"`).Replace(b3PublishedUpdate)
	now := vectorTime().Add(24 * time.Hour)
	_, err := s.decodeB3Lookup([]byte(raw), now)
	require.NoError(t, err)
	_, err = s.decodeB3Command([]byte(raw), true, now)
	require.Error(t, err)
}

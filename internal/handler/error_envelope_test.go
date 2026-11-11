package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tracerbiubiubiu/activelist/internal/apperr"
	"github.com/tracerbiubiubiu/zhuzhao-utils/errcode"
)

func TestFailureEnvelope_HidesServerDiagnostics(t *testing.T) {
	for _, status := range []int{500, 503} {
		code := apperr.CodeInternal
		if status == 503 {
			code = apperr.CodeDependency
		}
		err := apperr.New(status, code, "driver diagnostic SQLSTATE synthetic-private-host").WithDetail("internal_table", "private_table")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("request_id", "synthetic-request")
		Fail(c, err)
		require.Equal(t, status, w.Code)
		var body struct {
			Code      int
			Message   string
			RequestID string `json:"request_id"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, err.Num(), body.Code)
		want := errcode.ErrInternal.Message
		if status == 503 {
			want = errcode.ErrServiceUnavailable.Message
		}
		require.Equal(t, want, body.Message)
		require.Equal(t, "synthetic-request", body.RequestID)
		require.NotContains(t, w.Body.String(), "SQLSTATE")
		require.NotContains(t, w.Body.String(), "private")
		require.Same(t, err, c.Errors.Last().Err, "full diagnostic must remain available to server request-error logging")
	}
}

func TestFailureEnvelope_PreservesClientValidationContext(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	Fail(c, apperr.New(422, apperr.CodeValidation, "字段不合法").WithDetail("field", "public_field"))
	require.Equal(t, 422, w.Code)
	require.True(t, strings.Contains(w.Body.String(), "public_field"))
	require.Empty(t, c.Errors)
}

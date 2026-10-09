package audit_test

import (
	"context"
	"testing"
	"time"

	"backend-go/internal/pkg/audit"
	"backend-go/internal/pkg/telemetry"

	"github.com/stretchr/testify/assert"
)

func TestStructuredAuditor(t *testing.T) {
	auditor := audit.NewStructuredAuditor(nil)
	assert.NotNil(t, auditor)

	sc := telemetry.NewSpanContext()
	ctx := telemetry.ContextWithSpan(context.Background(), sc)

	event := audit.AuditEvent{
		ActorID:        "user-101",
		Action:         "user.update_password",
		TargetResource: "User",
		ResourceID:     "101",
		PayloadBefore:  map[string]string{"status": "active"},
		PayloadAfter:   map[string]string{"status": "active", "updated": "true"},
		IPAddress:      "192.168.1.1",
		CreatedAt:      time.Now(),
	}

	err := auditor.Log(ctx, event)
	assert.NoError(t, err)
}

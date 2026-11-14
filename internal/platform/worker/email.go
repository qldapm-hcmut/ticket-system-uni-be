package worker

import (
	"context"
	"time"

	"go.uber.org/fx"
)

type EmailJob interface {
	DeliverEmails(context.Context) (int64, error)
}

// RegisterEmailLifecycle starts an independent job. Deploy only one enabled
// email consumer; lifecycle event jobs retain their own database coordination.
func RegisterEmailLifecycle(lc fx.Lifecycle, email EmailJob) {
	register(lc, []job{{"email_outbox", 30 * time.Second, email.DeliverEmails}})
}

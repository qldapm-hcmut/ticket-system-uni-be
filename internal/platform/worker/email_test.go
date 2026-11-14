package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

type blockingEmailJob struct {
	started chan struct{}
	stopped chan struct{}
	calls   atomic.Int32
}

func (j *blockingEmailJob) DeliverEmails(ctx context.Context) (int64, error) {
	j.calls.Add(1)
	close(j.started)
	<-ctx.Done()
	close(j.stopped)
	return 0, ctx.Err()
}
func TestEmailJobStartsAndShutdownCancelsIt(t *testing.T) {
	job := &blockingEmailJob{started: make(chan struct{}), stopped: make(chan struct{})}
	app := fxtest.New(t, fx.Invoke(func(lc fx.Lifecycle) { RegisterEmailLifecycle(lc, job) }))
	app.RequireStart()
	select {
	case <-job.started:
	case <-time.After(time.Second):
		t.Fatal("email job did not start")
	}
	app.RequireStop()
	select {
	case <-job.stopped:
	default:
		t.Fatal("email job was not stopped")
	}
	if job.calls.Load() != 1 {
		t.Fatal("overlapping email job")
	}
}

type lifecycleJobs struct{ calls chan string }

func (j *lifecycleJobs) ExpireWaitlists(context.Context) (int64, error) {
	j.calls <- "expire"
	return 0, nil
}
func (j *lifecycleJobs) CompleteEvents(context.Context) (int64, error) {
	j.calls <- "complete"
	return 0, nil
}
func TestSlowEmailDoesNotBlockLifecycleJobs(t *testing.T) {
	email := &blockingEmailJob{started: make(chan struct{}), stopped: make(chan struct{})}
	jobs := &lifecycleJobs{calls: make(chan string, 2)}
	app := fxtest.New(t, fx.Invoke(func(lc fx.Lifecycle) { RegisterLifecycle(lc, jobs); RegisterEmailLifecycle(lc, email) }))
	app.RequireStart()
	defer app.RequireStop()
	for range 2 {
		select {
		case <-jobs.calls:
		case <-time.After(time.Second):
			t.Fatal("email blocked event lifecycle job")
		}
	}
}

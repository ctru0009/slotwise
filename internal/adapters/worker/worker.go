package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// errorTextLimit is how much of a handler error a job row keeps. Postgres text
// must be valid UTF-8, so the cut backs up to a rune boundary.
const errorTextLimit = 500

// Handler delivers one claimed job. Returning domain.ErrJobSkipped reports
// that there was deliberately nothing to deliver — a cancelled booking, an
// appointment that already started — and completes the job.
type Handler func(ctx context.Context, job domain.Job) error

// Deps are the worker's collaborators.
type Deps struct {
	Jobs    app.JobStore
	Handler Handler
	Clock   clock.Clock
	Logger  *slog.Logger // nil means slog.Default()
}

// Worker runs one job at a time, serially: claim, execute, apply the
// transition the outcome asks for, poll again.
type Worker struct {
	cfg  Config
	deps Deps
}

// New returns a worker for cfg and deps, refusing a configuration or a missing
// dependency that would otherwise only surface at runtime.
func New(cfg Config, deps Deps) (*Worker, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if deps.Jobs == nil {
		return nil, errors.New("worker needs a job store")
	}
	if deps.Handler == nil {
		return nil, errors.New("worker needs a handler")
	}
	if deps.Clock == nil {
		return nil, errors.New("worker needs a clock")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Worker{cfg: cfg, deps: deps}, nil
}

// Run claims and executes jobs until ctx is canceled. A claim failure never
// ends the loop — a database blip must not kill the worker — and a handler
// interrupted by shutdown is released, not retried.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		job, err := w.deps.Jobs.ClaimJob(ctx, w.cfg.WorkerID, w.deps.Clock.Now(), int(w.cfg.Lease/time.Second))
		switch {
		case err == nil:
			w.execute(ctx, job)
			continue
		case errors.Is(err, domain.ErrNotFound):
			// Nothing is due; the poll below waits out the interval.
		case ctx.Err() != nil:
			return nil
		default:
			w.deps.Logger.ErrorContext(ctx, "claiming a job", "worker", w.cfg.WorkerID, "error", err)
		}

		if !w.sleep(ctx, w.cfg.PollInterval) {
			return nil
		}
	}
}

// execute runs one claimed job and applies the transition its outcome asks
// for.
func (w *Worker) execute(ctx context.Context, job domain.Job) {
	if job.Attempts > w.cfg.MaxAttempts {
		// The budget was spent by an execution that died unreported — a
		// SIGKILL never runs the transition that would have counted it — so
		// this claim must not run the handler again.
		w.deadLetter(ctx, job, fmt.Sprintf("attempt budget of %d spent before this claim", w.cfg.MaxAttempts))
		return
	}

	w.deps.Logger.InfoContext(ctx, "job claimed", "job", job.ID, "kind", string(job.Kind), "attempt", job.Attempts)

	handlerCtx, cancel := context.WithTimeout(ctx, w.cfg.HandlerTimeout)
	err := w.deps.Handler(handlerCtx, job)
	cancel()

	switch {
	case err == nil || errors.Is(err, domain.ErrJobSkipped):
		w.complete(ctx, job)
	case ctx.Err() != nil:
		// The worker's context only cancels on shutdown, and the job that
		// cancellation interrupted belongs back in the queue now, not after a
		// backoff.
		w.release(ctx, job)
	case job.Attempts >= w.cfg.MaxAttempts:
		w.deadLetter(ctx, job, errorText(err))
	default:
		w.retry(ctx, job, errorText(err))
	}
}

// complete marks the job done.
func (w *Worker) complete(ctx context.Context, job domain.Job) {
	writeCtx, cancel := w.transitionCtx(ctx)
	defer cancel()

	applied, err := w.deps.Jobs.CompleteJob(writeCtx, w.cfg.WorkerID, job)
	if w.settled(writeCtx, "complete", job, applied, err) {
		w.deps.Logger.InfoContext(writeCtx, "job done", "job", job.ID, "kind", string(job.Kind))
	}
}

// release hands an interrupted job back.
func (w *Worker) release(ctx context.Context, job domain.Job) {
	writeCtx, cancel := w.transitionCtx(ctx)
	defer cancel()

	applied, err := w.deps.Jobs.ReleaseJob(writeCtx, w.cfg.WorkerID, job)
	if w.settled(writeCtx, "release", job, applied, err) {
		w.deps.Logger.InfoContext(writeCtx, "job released", "job", job.ID)
	}
}

// retry sends a failed job back to the queue once its backoff has passed.
func (w *Worker) retry(ctx context.Context, job domain.Job, reason string) {
	writeCtx, cancel := w.transitionCtx(ctx)
	defer cancel()

	runAt := w.deps.Clock.Now().Add(w.backoff(job.Attempts))
	applied, err := w.deps.Jobs.RetryJob(writeCtx, w.cfg.WorkerID, job, runAt, reason)
	if w.settled(writeCtx, "retry", job, applied, err) {
		w.deps.Logger.WarnContext(writeCtx, "job retried", "job", job.ID, "attempt", job.Attempts, "run_at", runAt, "error", reason)
	}
}

// deadLetter marks a job dead: its attempt budget is spent, or the claim that
// produced it already overspent it.
func (w *Worker) deadLetter(ctx context.Context, job domain.Job, reason string) {
	writeCtx, cancel := w.transitionCtx(ctx)
	defer cancel()

	applied, err := w.deps.Jobs.DeadLetterJob(writeCtx, w.cfg.WorkerID, job, reason)
	if w.settled(writeCtx, "dead", job, applied, err) {
		w.deps.Logger.ErrorContext(writeCtx, "job dead", "job", job.ID, "attempt", job.Attempts, "error", reason)
	}
}

// transitionCtx is the context a guarded transition write runs on. Cancellation
// is shed — a shutdown must not drop the write that records the job's outcome,
// or the job would stay leased until expiry and be delivered twice — while the
// deadline keeps an unresponsive database from holding the process open.
func (w *Worker) transitionCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), transitionTimeout)
}

// settled reports one guarded transition. A write failure is an error; an
// unapplied write means the lease moved on — another worker reclaimed an
// expired lease — which is a warning, not a failure of this worker.
func (w *Worker) settled(ctx context.Context, transition string, job domain.Job, applied bool, err error) bool {
	switch {
	case err != nil:
		w.deps.Logger.ErrorContext(ctx, "transitioning a job", "job", job.ID, "transition", transition, "error", err)
		return false
	case !applied:
		w.deps.Logger.WarnContext(ctx, "lease moved on", "job", job.ID, "transition", transition)
		return false
	default:
		return true
	}
}

// backoff is how long a failed job waits before it is claimed again: the base
// doubled once per attempt so far, capped. The loop stops as soon as the cap
// is reached so the multiply can never wrap.
func (w *Worker) backoff(attempts int) time.Duration {
	d := w.cfg.BackoffBase
	for i := 1; i < attempts; i++ {
		if d >= w.cfg.BackoffCap/2 {
			return w.cfg.BackoffCap
		}
		d *= 2
	}
	return min(d, w.cfg.BackoffCap)
}

// sleep waits for d, reporting false when ctx was canceled first.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// errorText bounds an error for the job row's last_error. A multi-byte rune
// straddling the cut would otherwise leave the stored text invalid UTF-8.
func errorText(err error) string {
	text := err.Error()
	if len(text) <= errorTextLimit {
		return text
	}
	cut := errorTextLimit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

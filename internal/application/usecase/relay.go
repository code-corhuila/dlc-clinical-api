package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// producer is the envelope producer of every Clinical event.
const producer = "clinical-service"

// RelayService publishes committed outbox events (Norma 5.3.11) in bounded
// runs that are safe to repeat (Anexo D): delivery is at least once and every
// consumer deduplicates by eventId.
type RelayService struct {
	Outbox      out.OutboxStore
	Publisher   out.Publisher
	Clock       out.Clock
	Random      func() float64 // [0,1), injectable for tests
	BatchSize   int
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// RunReport counts the outcome of one run.
type RunReport struct{ Published, Retried, Failed int }

// RunOnce reads at most BatchSize due events and publishes them in order. A
// failing item does not stop the batch; an expired context stops the run.
func (s *RelayService) RunOnce(ctx context.Context) (RunReport, error) {
	var report RunReport
	due, err := s.Outbox.Due(ctx, s.Clock.Now(), s.BatchSize)
	if err != nil {
		return report, err
	}
	for _, p := range due {
		if ctx.Err() != nil {
			break
		}
		err := s.Publisher.Publish(ctx, envelope(p.Event))
		now := s.Clock.Now()
		attempts := p.Attempts + 1
		switch {
		case err == nil:
			if err := s.Outbox.MarkPublished(ctx, p.Event.EventID, now); err != nil {
				return report, err
			}
			report.Published++
		case errors.Is(err, out.ErrPermanentPublish) || attempts >= s.MaxAttempts:
			if err := s.Outbox.MarkFailed(ctx, p.Event.EventID, attempts, safeReason(err)); err != nil {
				return report, err
			}
			report.Failed++
		default:
			if err := s.Outbox.Reschedule(ctx, p.Event.EventID, attempts, now.Add(s.backoff(attempts)), safeReason(err)); err != nil {
				return report, err
			}
			report.Retried++
		}
	}
	return report, nil
}

// backoff is full jitter: a random wait between 0 and min(MaxDelay, BaseDelay*2^(attempt-1)).
func (s *RelayService) backoff(attempt int) time.Duration {
	ceiling := s.MaxDelay
	if attempt-1 < 32 {
		if exp := s.BaseDelay << (attempt - 1); exp > 0 && exp < ceiling {
			ceiling = exp
		}
	}
	return time.Duration(s.Random() * float64(ceiling))
}

func envelope(e out.OutboxEvent) out.Envelope {
	return out.Envelope{EventID: e.EventID, EventType: e.EventType, AggregateID: e.AggregateID, AggregateType: e.AggregateType,
		OccurredAt: e.OccurredAt.UTC().Format(time.RFC3339), Version: e.EventVersion, Producer: producer, Payload: e.Payload,
		Metadata: out.Metadata{CorrelationID: e.CorrelationID, CausationID: e.CausationID, ActorUserID: e.ActorID}}
}

// safeReason bounds the stored reason to the outbox validator's 500 characters.
func safeReason(err error) string {
	r := []rune(err.Error())
	if len(r) > 500 {
		r = r[:500]
	}
	return string(r)
}

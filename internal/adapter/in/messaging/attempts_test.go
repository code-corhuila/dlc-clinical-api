package messaging

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestPriorAttemptsReadsEitherBrokerCounter(t *testing.T) {
	for name, c := range map[string]struct {
		headers amqp.Table
		want    int64
	}{
		"first delivery":         {nil, 0},
		"rabbitmq 3 failures":    {amqp.Table{"x-delivery-count": int64(3)}, 3},
		"rabbitmq 4 requeues":    {amqp.Table{"x-acquired-count": int64(4)}, 4},
		"both, larger wins":      {amqp.Table{"x-delivery-count": int64(1), "x-acquired-count": int64(5)}, 5},
		"32-bit counter":         {amqp.Table{"x-acquired-count": int32(2)}, 2},
		"unexpected value types": {amqp.Table{"x-acquired-count": "7"}, 0},
	} {
		if got := priorAttempts(c.headers); got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
}

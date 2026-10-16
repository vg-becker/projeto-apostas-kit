package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newWorker(t *testing.T, config Config, task Task) (*Worker, *bytes.Buffer) {
	t.Helper()

	var logs bytes.Buffer

	worker, err := New(config, task, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	return worker, &logs
}

func stop(t *testing.T, worker *Worker) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop error = %v", err)
	}

	select {
	case <-worker.Done():
	default:
		t.Fatal("Done must be closed once Stop returns")
	}
}

func TestInvalidConfigIsRefused(t *testing.T) {
	noop := func(context.Context) (bool, error) { return false, nil }

	for name, config := range map[string]Config{
		"no name":     {Interval: time.Second},
		"no interval": {Name: "w"},
	} {
		if _, err := New(config, noop, nil); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s: error = %v, want %v", name, err, ErrInvalidConfig)
		}
	}

	if _, err := New(Config{Name: "w", Interval: time.Second}, nil, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("no task: error = %v, want %v", err, ErrInvalidConfig)
	}
}

func TestTheTaskRunsPeriodicallyUntilStopped(t *testing.T) {
	var calls atomic.Int32

	worker, _ := newWorker(t, Config{Name: "w", Interval: 10 * time.Millisecond}, func(context.Context) (bool, error) {
		calls.Add(1)

		return false, nil
	})

	if err := worker.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	stop(t, worker)

	atStop := calls.Load()
	time.Sleep(100 * time.Millisecond)

	if atStop < 5 || calls.Load() != atStop {
		t.Fatalf("calls at stop = %d, later = %d: it must run periodically and then stay stopped", atStop, calls.Load())
	}
}

func TestMoreWorkRepeatsWithoutWaiting(t *testing.T) {
	var calls atomic.Int32

	worker, _ := newWorker(t, Config{Name: "w", Interval: time.Hour}, func(context.Context) (bool, error) {
		return calls.Add(1) < 50, nil
	})

	if err := worker.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 50 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	stop(t, worker)

	if calls.Load() != 50 {
		t.Fatalf("calls = %d, want 50 back-to-back iterations despite the 1h interval", calls.Load())
	}
}

func TestErrorsBackOffAndASuccessResetsTheInterval(t *testing.T) {
	var (
		mutex sync.Mutex
		times []time.Time
	)

	worker, _ := newWorker(
		t,
		Config{Name: "w", Interval: 20 * time.Millisecond, MaxErrorInterval: 160 * time.Millisecond},
		func(context.Context) (bool, error) {
			mutex.Lock()
			defer mutex.Unlock()

			times = append(times, time.Now())
			if len(times) <= 4 {
				return false, errors.New("boom")
			}

			return false, nil
		},
	)

	if err := worker.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	time.Sleep(900 * time.Millisecond)
	stop(t, worker)

	mutex.Lock()
	defer mutex.Unlock()

	if len(times) < 7 {
		t.Fatalf("only %d iterations", len(times))
	}

	wantAtLeast := []time.Duration{40, 80, 160, 160}
	for index, minimum := range wantAtLeast {
		if gap := times[index+1].Sub(times[index]); gap < minimum*time.Millisecond {
			t.Fatalf("gap %d after an error = %v, want at least %v", index, gap, minimum*time.Millisecond)
		}
	}

	if gap := times[6].Sub(times[5]); gap > 100*time.Millisecond {
		t.Fatalf("gap after recovery = %v, the interval must go back to normal", gap)
	}
}

func TestStopWaitsForTheIterationInProgress(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	worker, _ := newWorker(t, Config{Name: "w", Interval: time.Millisecond}, func(context.Context) (bool, error) {
		select {
		case entered <- struct{}{}:
		default:
		}

		<-release

		return false, nil
	})

	if err := worker.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	<-entered

	stopped := make(chan error, 1)
	go func() { stopped <- worker.Stop(context.Background()) }()

	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) while an iteration was running", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	if err := <-stopped; err != nil {
		t.Fatalf("Stop error = %v", err)
	}
}

func TestAStopDeadlineCancelsTheIteration(t *testing.T) {
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{})

	worker, _ := newWorker(t, Config{Name: "w", Interval: time.Millisecond}, func(ctx context.Context) (bool, error) {
		entered <- struct{}{}
		<-ctx.Done()
		close(canceled)

		return false, ctx.Err()
	})

	if err := worker.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := worker.Stop(ctx)

	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("the iteration context must be canceled when the deadline passes")
	}

	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("error = %v, want %v", err, ErrShutdownTimeout)
	}

	select {
	case <-worker.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the worker must terminate after the abandon")
	}
}

func TestLifecycleMisuseIsHarmless(t *testing.T) {
	worker, _ := newWorker(t, Config{Name: "w", Interval: time.Millisecond}, func(context.Context) (bool, error) {
		return false, nil
	})

	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop before Start error = %v", err)
	}

	if err := worker.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	if err := worker.Start(); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start error = %v, want %v", err, ErrAlreadyStarted)
	}

	stop(t, worker)

	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop error = %v", err)
	}
}

func TestAPanicInTheTaskIsRecoveredLoggedWithoutItsValueAndTheLoopContinues(t *testing.T) {
	var calls atomic.Int32

	worker, logs := newWorker(t, Config{Name: "w", Interval: 5 * time.Millisecond}, func(context.Context) (bool, error) {
		if calls.Add(1) == 1 {
			panic("secret-token-in-the-panic")
		}

		return false, nil
	})

	if err := worker.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	stop(t, worker)

	if calls.Load() < 3 {
		t.Fatalf("calls = %d: the loop must survive a panic", calls.Load())
	}

	if strings.Contains(logs.String(), "secret-token-in-the-panic") || !strings.Contains(logs.String(), "task panicked") {
		t.Fatalf("log = %s", logs.String())
	}
}

func TestDoublingStopsAtTheMaximum(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: time.Second},
		{attempts: 1, want: 2 * time.Second},
		{attempts: 2, want: 4 * time.Second},
		{attempts: 3, want: 5 * time.Second},
		{attempts: 50, want: 5 * time.Second},
	}

	for _, test := range tests {
		if got := doubling(time.Second, 5*time.Second, test.attempts); got != test.want {
			t.Fatalf("doubling(%d) = %v, want %v", test.attempts, got, test.want)
		}
	}
}

func TestJitterOnlyLengthensTheWaitWithinTheFraction(t *testing.T) {
	worker, _ := newWorker(t, Config{Name: "w", Interval: time.Second, Jitter: DefaultJitter}, func(context.Context) (bool, error) {
		return false, nil
	})

	for range 200 {
		delay := worker.withJitter(time.Second)

		if delay < time.Second || delay > 1100*time.Millisecond {
			t.Fatalf("delay = %v, want within [1s, 1.1s]", delay)
		}
	}

	if got := (&Worker{}).withJitter(time.Second); got != time.Second {
		t.Fatalf("no jitter configured = %v", got)
	}
}

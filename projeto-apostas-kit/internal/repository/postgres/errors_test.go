package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"apostas-go/internal/model/port"
)

func TestTranslateErrorMapsToPorts(t *testing.T) {
	tests := []struct {
		name  string
		input error
		want  error
	}{
		{name: "no rows", input: pgx.ErrNoRows, want: port.ErrNotFound},
		{
			name:  "unique violation",
			input: &pgconn.PgError{Code: "23505", ConstraintName: "some_unique"},
			want:  port.ErrAlreadyExists,
		},
		{
			name:  "wrapped unique violation",
			input: fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505"}),
			want:  port.ErrAlreadyExists,
		},
		{name: "serialization failure", input: &pgconn.PgError{Code: "40001"}, want: port.ErrTransient},
		{name: "deadlock", input: &pgconn.PgError{Code: "40P01"}, want: port.ErrTransient},
		{name: "connection class", input: &pgconn.PgError{Code: "08006"}, want: port.ErrTransient},
		{name: "too many connections", input: &pgconn.PgError{Code: "53300"}, want: port.ErrTransient},
		{name: "admin shutdown", input: &pgconn.PgError{Code: "57P01"}, want: port.ErrTransient},
		{name: "network error", input: &net.OpError{Op: "dial"}, want: port.ErrTransient},
		{name: "unexpected eof", input: io.ErrUnexpectedEOF, want: port.ErrTransient},
		{name: "deadline", input: context.DeadlineExceeded, want: port.ErrTransient},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := translateError(test.input)

			if !errors.Is(got, test.want) || !errors.Is(got, test.input) {
				t.Fatalf("error = %v, want %v with the original reachable", got, test.want)
			}
		})
	}
}

func TestTranslateErrorKeepsPermanentErrorsPermanent(t *testing.T) {
	inputs := []error{
		&pgconn.PgError{Code: "23514", ConstraintName: "wallets_balance_not_negative"},
		&pgconn.PgError{Code: "23503"},
		context.Canceled,
		errors.New("boom"),
	}

	for _, input := range inputs {
		got := translateError(input)

		if errors.Is(got, port.ErrTransient) || !errors.Is(got, input) {
			t.Fatalf("error = %v, want %v unchanged and permanent", got, input)
		}
	}

	if translateError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestConstraintName(t *testing.T) {
	err := translateError(&pgconn.PgError{Code: "23505", ConstraintName: "idempotency_unique"})

	if constraintName(err) != "idempotency_unique" {
		t.Fatalf("constraint = %q", constraintName(err))
	}

	if constraintName(errors.New("plain")) != "" {
		t.Fatal("non database errors have no constraint")
	}
}

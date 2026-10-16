package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"apostas-go/internal/model/port"
)

const (
	sqlStateUniqueViolation      = "23505"
	sqlStateSerializationFailure = "40001"
	sqlStateDeadlockDetected     = "40P01"
	sqlStateConnectionClass      = "08"
	sqlStateResourcesClass       = "53"
	sqlStateOperatorClass        = "57P"
)

// translateError converte erros do pgx nos erros das portas do model. Falhas de
// infraestrutura recuperáveis viram port.ErrTransient; violações de regra ficam permanentes.
func translateError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", port.ErrNotFound, err)
	}

	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		return translateDatabaseError(databaseError, err)
	}

	if isConnectionFailure(err) {
		return fmt.Errorf("%w: %w", port.ErrTransient, err)
	}

	return err
}

func translateDatabaseError(databaseError *pgconn.PgError, err error) error {
	if databaseError.Code == sqlStateUniqueViolation {
		return fmt.Errorf(
			"%w: constraint %s: %w",
			port.ErrAlreadyExists,
			databaseError.ConstraintName,
			err,
		)
	}

	if isTransientState(databaseError.Code) {
		return fmt.Errorf("%w: %w", port.ErrTransient, err)
	}

	return err
}

func isTransientState(code string) bool {
	return code == sqlStateSerializationFailure ||
		code == sqlStateDeadlockDetected ||
		strings.HasPrefix(code, sqlStateConnectionClass) ||
		strings.HasPrefix(code, sqlStateResourcesClass) ||
		strings.HasPrefix(code, sqlStateOperatorClass)
}

func isConnectionFailure(err error) bool {
	var networkError net.Error

	return pgconn.SafeToRetry(err) ||
		errors.As(err, &networkError) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded)
}

// constraintName devolve a constraint violada, ou vazio se o erro não vier do banco.
func constraintName(err error) string {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		return databaseError.ConstraintName
	}

	return ""
}

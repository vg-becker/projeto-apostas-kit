package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// LookupFunc lê uma variável de ambiente, no formato de os.LookupEnv.
type LookupFunc func(name string) (string, bool)

// reader lê variáveis acumulando os erros, para que a partida informe todos de uma vez.
// As mensagens citam só o nome da variável, nunca o valor: ele pode ser um segredo.
type reader struct {
	lookup   LookupFunc
	problems []error
}

func (source *reader) text(name string, fallback string) string {
	value, found := source.lookup(name)
	value = strings.TrimSpace(value)

	if !found || value == "" {
		return fallback
	}

	return value
}

func (source *reader) required(name string) string {
	value := source.text(name, "")
	if value == "" {
		source.fail(fmt.Errorf("%s is required", name))
	}

	return value
}

func (source *reader) duration(name string, fallback time.Duration) time.Duration {
	raw := source.text(name, "")
	if raw == "" {
		return fallback
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		source.fail(fmt.Errorf("%s must be a positive duration such as 30s", name))

		return fallback
	}

	return parsed
}

func (source *reader) boolean(name string, fallback bool) bool {
	raw := source.text(name, "")
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		source.fail(fmt.Errorf("%s must be true or false", name))

		return fallback
	}

	return parsed
}

func (source *reader) fail(problem error) {
	source.problems = append(source.problems, problem)
}

func (source *reader) failWith(context string, problem error) {
	if problem != nil {
		source.fail(fmt.Errorf("%s: %w", context, problem))
	}
}

func (source *reader) err() error {
	if len(source.problems) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(source.problems...))
}

package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	// Os drivers do migrate: o do Postgres via pgx (que toma um lock consultivo, então várias
	// instâncias subindo juntas aplicam cada migration uma só vez) e a leitura de arquivos.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// Migrate aplica as migrations pendentes do diretório. Sem nada a aplicar, não faz nada. Um
// banco marcado como "dirty" (migration que falhou no meio) recusa novas migrations até
// alguém corrigi-lo: o serviço não adivinha o estado real.
func Migrate(databaseURL string, directory string) error {
	migrator, err := migrate.New("file://"+directory, migrationURL(databaseURL))
	if err != nil {
		return fmt.Errorf("postgres: open migrations: %w", err)
	}

	defer func() { _, _ = migrator.Close() }()

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: apply migrations: %w", err)
	}

	return nil
}

// migrationURL troca o esquema para o do driver pgx do migrate.
func migrationURL(databaseURL string) string {
	for _, scheme := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(databaseURL, scheme) {
			return "pgx5://" + strings.TrimPrefix(databaseURL, scheme)
		}
	}

	return databaseURL
}

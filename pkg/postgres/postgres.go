package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresCfg struct {
	Host     string `yaml:"POSTGRES_HOST" env:"POSTGRES_HOST" env-default:"localhost"`
	Port     string `yaml:"POSTGRES_PORT" env:"POSTGRES_PORT" env-default:"5432"`
	Username string `yaml:"POSTGRES_USER" env:"POSTGRES_USER" env-default:"admin"`
	Password string `yaml:"POSTGRES_PASS" env:"POSTGRES_PASS" env-default:"admin"`
	Database string `yaml:"POSTGRES_DB" env:"POSTGRES_DB" env-default:"test_db"`
	Sslmode  string `yaml:"POSTGRES_SSLMODE" env:"POSTGRES_SSLMODE" env-default:"disable"`
	MaxConn  int32  `yaml:"POSTGRES_MAX_CONN" env:"POSTGRES_MAX_CONN" env-default:"10"`
	MinConn  int32  `yaml:"POSTGRES_MIN_CONN" env:"POSTGRES_MIN_CONN" env-default:"5"`
}

func New(ctx context.Context, config PostgresCfg) (*pgxpool.Pool, error) {
	// urlExample := "postgres://username:password@localhost:5432/database_name?sslmode=%s&pool_max_conns=%d&pool_min_conns=%d"
	connString := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s&pool_max_conns=%d&pool_min_conns=%d",
		config.Username,
		config.Password,
		config.Host,
		config.Port,
		config.Database,
		config.Sslmode,
		config.MaxConn,
		config.MinConn,
	)

	conn, err := pgxpool.New(ctx, connString)

	if err != nil {
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}

	// Применям миграции
	m, err := migrate.New(
		"file://db/migrations",
		fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
			config.Username,
			config.Password,
			config.Host,
			config.Port,
			config.Database,
			config.Sslmode,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to migrate to database: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return nil, fmt.Errorf("failed to migrate to database: %w", err)
	}

	return conn, nil
}

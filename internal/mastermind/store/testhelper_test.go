package store

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var testDSN string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("pulpo"),
		postgres.WithUsername("pulpo"),
		postgres.WithPassword("pulpo"),
		postgres.BasicWaitStrategies(),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		log.Fatalf("postgres.Run: %v", err)
	}
	defer func() { _ = ctr.Terminate(ctx) }()

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("ConnectionString: %v", err)
		return 1
	}
	testDSN = dsn

	// Apply migrations.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")
	mg, err := migrate.New("file://"+migrationsDir, dsn)
	if err != nil {
		log.Printf("migrate.New: %v", err)
		return 1
	}
	if err := mg.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Printf("migrate.Up: %v", err)
		return 1
	}

	return m.Run()
}

// newPool returns a clean pool. The caller may truncate tables between tests.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	s, err := Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s.Pool()
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE TABLE tasks CASCADE")
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// Package migrations runs tern schema migrations at application startup
// using SQL files embedded directly into the binary. No tern.conf, no
// sidecar binary, no Docker entrypoint script — just a function the app
// calls before it opens its main connection pool.
package migrations

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/tern/v2/migrate"
)

// EmbedFS ships every file under ./migrate/ (SQL migrations) inside the
// compiled binary. Tern's LoadMigrations is given the sub-fs rooted at
// "migrate" so it sees the *.sql files at the top level, which is its
// expected layout.
//
//go:embed all:migrate
var EmbedFS embed.FS

// Run applies every pending migration. It opens its own short-lived
// *pgx.Conn (tern requires a single connection, not a pool), then closes
// it before returning so the caller's pool never competes with the
// migrator. Cancelling ctx aborts the in-flight migration server-side via
// pg's CancelRequest protocol, which matters when a long DDL would
// otherwise keep running after the process is killed.
func Run(ctx context.Context, dsn string, log *slog.Logger) error {
	pgxCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	// QueryExecModeExec avoids the extended-protocol parse/bind/execute
	// split, which tern's multi-statement migrations don't benefit from.
	pgxCfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	pgxCfg.RuntimeParams["application_name"] = "animelisterr-migrator"

	// Aggressive TCP keepalives: if the process is killed mid-migration,
	// Postgres detects the dead connection in ~90s instead of the default
	// 20+ minutes, freeing locks and rolling back the open transaction.
	pgxCfg.RuntimeParams["tcp_keepalives_idle"] = "60"
	pgxCfg.RuntimeParams["tcp_keepalives_interval"] = "10"
	pgxCfg.RuntimeParams["tcp_keepalives_count"] = "3"

	// Forward any RAISE NOTICE output from migrations to our logger so
	// operators can see progress information inside long migrations.
	pgxCfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		if log != nil {
			log.Info("migrate notice", "message", n.Message)
		}
	}

	conn, err := pgx.ConnectConfig(ctx, pgxCfg)
	if err != nil {
		return err
	}

	// On ctx cancellation, send a Postgres-level cancel request so the
	// currently-executing statement is aborted server-side instead of
	// being allowed to run to completion after the client goes away.
	go func() {
		<-ctx.Done()
		if err := conn.PgConn().CancelRequest(context.Background()); err != nil && log != nil {
			log.Warn("cancel migration", "err", err)
		}
		_ = conn.Close(context.Background())
	}()
	defer conn.Close(context.Background())

	if log != nil {
		log.Info("migrator connected")
	}

	migrator, err := migrate.NewMigrator(ctx, conn, "public.schema_version")
	if err != nil {
		return err
	}
	// Log each statement as it runs; failures include both our log line
	// and tern's returned MigrationPgError.
	migrator.OnStart = func(seq int32, name, direction, _ string) {
		if log != nil {
			log.Info("migrate", "seq", seq, "name", name, "dir", direction)
		}
	}

	sub, err := fs.Sub(EmbedFS, "migrate")
	if err != nil {
		return err
	}
	if err := migrator.LoadMigrations(sub); err != nil {
		return err
	}

	before, err := migrator.GetCurrentVersion(ctx)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("running migrations", "start_version", before)
	}
	if err := migrator.Migrate(ctx); err != nil {
		return err
	}
	after, err := migrator.GetCurrentVersion(ctx)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("migrations complete", "end_version", after)
	}
	return nil
}

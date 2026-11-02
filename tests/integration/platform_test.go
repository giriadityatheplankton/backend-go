//go:build integration

package integration_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"backend-go/internal/pkg/database"
	"backend-go/internal/pkg/database/migrate"
	"backend-go/internal/pkg/messaging"
	"backend-go/internal/pkg/messaging/inbox"
	"backend-go/internal/pkg/outbox"
	"backend-go/migrations"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrations_ConcurrentRunsChecksumAndDown(t *testing.T) {
	dsn := newDatabase(t)
	db := openDB(t, dsn)
	ctx := context.Background()
	eng := migrate.NewEngine(migrations.Sources()...)

	// Several pods/Jobs starting at once must serialise on the advisory lock.
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- eng.Run(ctx, openDB(t, dsn)) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, eng.Run(ctx, db), "re-running is a no-op")

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM schema_history WHERE source='platform'`).Scan(&n))
	assert.GreaterOrEqual(t, n, 5)

	// Editing an applied migration is detected.
	tampered := fstest.MapFS{"000001_create_outbox_table.up.sql": {Data: []byte("SELECT 1; -- changed")}}
	err := migrate.NewEngine(migrate.Source{Name: "platform", FS: tampered, Dir: "."}).Run(ctx, db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")

	// Down reverts the newest migration only.
	require.NoError(t, eng.Down(ctx, db, "platform", 1))
	var exists bool
	require.NoError(t, db.QueryRow(`SELECT to_regclass('inbox_messages') IS NOT NULL`).Scan(&exists))
	assert.False(t, exists)
	require.NoError(t, eng.Run(ctx, db))
	require.NoError(t, db.QueryRow(`SELECT to_regclass('inbox_messages') IS NOT NULL`).Scan(&exists))
	assert.True(t, exists)
}

func TestMigrations_NoTransactionMarkerAllowsConcurrentIndex(t *testing.T) {
	dsn := newDatabase(t)
	db := openDB(t, dsn)
	fs := fstest.MapFS{
		"000001_t.up.sql":   {Data: []byte("CREATE TABLE t (id int);")},
		"000002_idx.up.sql": {Data: []byte("-- +migrate notx\nCREATE INDEX CONCURRENTLY idx_t_id ON t (id);")},
	}
	require.NoError(t, migrate.NewEngine(migrate.Source{Name: "x", FS: fs, Dir: "."}).Run(context.Background(), db))
	var ok bool
	require.NoError(t, db.QueryRow(`SELECT to_regclass('idx_t_id') IS NOT NULL`).Scan(&ok))
	assert.True(t, ok)
}

func ev(id, agg string, at time.Time) *outbox.OutboxEvent {
	return &outbox.OutboxEvent{ID: id, AggregateType: "order", AggregateID: agg, EventType: "order.created",
		Payload: []byte(`{"x":1}`), Headers: map[string]string{"k": "v"}, CreatedAt: at}
}

func TestOutboxPostgres_SaveClaimFenceAndReclaim(t *testing.T) {
	db, _ := migrated(t, migrations.Sources()[0])
	ctx := context.Background()
	repo := outbox.NewPostgresRepository(db)
	now := time.Now().Add(-time.Minute)

	require.NoError(t, repo.Save(ctx, ev("e1", "A", now)))
	require.NoError(t, repo.Save(ctx, ev("e2", "A", now.Add(time.Second))))
	require.NoError(t, repo.Save(ctx, ev("x1", "B", now.Add(2*time.Second))))

	// Per-aggregate order: e2 must wait for e1.
	got, err := repo.Claim(ctx, 0, 10, "podA", time.Minute, true)
	require.NoError(t, err)
	ids := []string{}
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	assert.ElementsMatch(t, []string{"e1", "x1"}, ids)
	assert.Equal(t, map[string]string{"k": "v"}, got[0].Headers)

	// Another pod cannot take leased events (SKIP LOCKED semantics + status).
	other, err := repo.Claim(ctx, 0, 10, "podB", time.Minute, true)
	require.NoError(t, err)
	assert.Empty(t, other)

	// A foreign owner cannot mark.
	assert.ErrorIs(t, repo.MarkPublished(ctx, "e1", "podB"), outbox.ErrLeaseLost)
	require.NoError(t, repo.MarkPublished(ctx, "e1", "podA"))

	// Save of an already-published id must not reset it.
	require.NoError(t, repo.Save(ctx, ev("e1", "A", now)))
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM outbox_events WHERE id='e1'`).Scan(&status))
	assert.Equal(t, "PUBLISHED", status)

	// e2 is now claimable.
	next, err := repo.Claim(ctx, 0, 10, "podA", time.Minute, true)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, "e2", next[0].ID)

	// Crash recovery: expire x1's lease and let podB reclaim it.
	_, err = db.Exec(`UPDATE outbox_events SET locked_until = now() - interval '1 second' WHERE id='x1'`)
	require.NoError(t, err)
	re, err := repo.Claim(ctx, 0, 10, "podB", time.Minute, true)
	require.NoError(t, err)
	require.Len(t, re, 1)
	assert.Equal(t, "x1", re[0].ID)
	assert.ErrorIs(t, repo.MarkPublished(ctx, "x1", "podA"), outbox.ErrLeaseLost, "the crashed owner is fenced out")

	// Failure -> DLQ in one statement.
	require.NoError(t, repo.MoveToDLQ(ctx, "x1", "podB", "boom"))
	var dlq int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM dlq_events WHERE original_event_id='x1'`).Scan(&dlq))
	assert.Equal(t, 1, dlq)

	st, err := repo.Stats(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, st.Unpublished) // e2 still processing
	assert.Greater(t, st.OldestPendingAge, time.Duration(0))

	require.NoError(t, repo.ReleaseLease(ctx, "podA", []string{"e2"}))
	again, _ := repo.Claim(ctx, 0, 10, "podC", time.Minute, true)
	assert.Len(t, again, 1, "released event is claimable again")
}

func TestOutboxPostgres_ConcurrentClaimersNeverOverlap(t *testing.T) {
	db, _ := migrated(t, migrations.Sources()[0])
	ctx := context.Background()
	repo := outbox.NewPostgresRepository(db)
	for i := 0; i < 200; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+i/676))
		require.NoError(t, repo.Save(ctx, ev(id, id, time.Now().Add(-time.Hour))))
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			owner := string(rune('A' + w))
			for {
				got, err := repo.Claim(ctx, 0, 7, owner, time.Minute, false)
				assert.NoError(t, err)
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, e := range got {
					seen[e.ID]++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	assert.Len(t, seen, 200)
	for id, n := range seen {
		assert.Equal(t, 1, n, "event %s claimed by %d workers", id, n)
	}
}

func TestInbox_ExactlyOnceAcrossRedeliveriesAndRollback(t *testing.T) {
	db, _ := migrated(t, migrations.Sources()[0])
	ctx := context.Background()
	tx := database.NewTxManager(db)
	_, err := db.Exec(`CREATE TABLE effects (id text)`)
	require.NoError(t, err)

	ib := inbox.New(tx, db, "svc/orders", nil)
	var calls int32
	fail := atomic.Bool{}
	h := ib.Wrap(func(ctx context.Context, m messaging.Message) error {
		atomic.AddInt32(&calls, 1)
		if _, err := database.Executor(ctx, db).ExecContext(ctx, `INSERT INTO effects VALUES ($1)`, m.ID); err != nil {
			return err
		}
		if fail.Load() {
			return errors.New("downstream failed after writing")
		}
		return nil
	})

	// A failing handler rolls back BOTH the effect and the inbox row, so a retry can succeed.
	fail.Store(true)
	require.Error(t, h(ctx, messaging.Message{ID: "m1"}))
	var n int
	require.NoError(t, db.QueryRow(`SELECT (SELECT count(*) FROM effects), (SELECT count(*) FROM inbox_messages)`).Scan(&n, new(int)))
	assert.Equal(t, 0, n)

	// 20 concurrent deliveries of the same message apply the effect once.
	fail.Store(false)
	atomic.StoreInt32(&calls, 0)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); assert.NoError(t, h(ctx, messaging.Message{ID: "m2"})) }()
	}
	wg.Wait()
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM effects WHERE id='m2'`).Scan(&n))
	assert.Equal(t, 1, n)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "handler body ran once")

	// A different consumer name has an independent inbox.
	other := inbox.New(tx, db, "svc/other", nil).Wrap(func(context.Context, messaging.Message) error { atomic.AddInt32(&calls, 1); return nil })
	require.NoError(t, other(ctx, messaging.Message{ID: "m2"}))
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))

	purged, err := ib.Purge(ctx, time.Now().Add(time.Hour), 100)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, purged, int64(1))
}

func TestTxManager_CommitRollbackJoinAndPanic(t *testing.T) {
	db, _ := migrated(t)
	ctx := context.Background()
	_, err := db.Exec(`CREATE TABLE kv (k text primary key)`)
	require.NoError(t, err)
	tx := database.NewTxManager(db)
	count := func() int { var n int; require.NoError(t, db.QueryRow(`SELECT count(*) FROM kv`).Scan(&n)); return n }

	require.NoError(t, tx.WithinTx(ctx, func(ctx context.Context) error {
		_, err := database.Executor(ctx, db).ExecContext(ctx, `INSERT INTO kv VALUES ('a')`)
		return err
	}))
	assert.Equal(t, 1, count())

	boom := errors.New("boom")
	assert.ErrorIs(t, tx.WithinTx(ctx, func(ctx context.Context) error {
		_, _ = database.Executor(ctx, db).ExecContext(ctx, `INSERT INTO kv VALUES ('b')`)
		return boom
	}), boom)
	assert.Equal(t, 1, count(), "rolled back")

	// Nested call joins the outer transaction: inner error rolls back the outer work too.
	assert.Error(t, tx.WithinTx(ctx, func(ctx context.Context) error {
		_, _ = database.Executor(ctx, db).ExecContext(ctx, `INSERT INTO kv VALUES ('c')`)
		return tx.WithinTx(ctx, func(ctx context.Context) error {
			assert.True(t, database.InTx(ctx))
			return boom
		})
	}))
	assert.Equal(t, 1, count())

	assert.Panics(t, func() {
		_ = tx.WithinTx(ctx, func(ctx context.Context) error {
			_, _ = database.Executor(ctx, db).ExecContext(ctx, `INSERT INTO kv VALUES ('d')`)
			panic("oops")
		})
	})
	assert.Equal(t, 1, count(), "panic rolls back")
}

func TestTxManager_RetriesSerializationFailure(t *testing.T) {
	db, _ := migrated(t)
	ctx := context.Background()
	_, err := db.Exec(`CREATE TABLE acct (id int primary key, bal int); INSERT INTO acct VALUES (1,100),(2,100)`)
	require.NoError(t, err)
	tx := database.NewTxManager(db).WithOptions(&sqlTxSerializable)

	// Two serializable transactions that read each other's rows and write: one gets 40001 and is retried.
	var attempts int32
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := tx.WithinTx(ctx, func(ctx context.Context) error {
				n := atomic.AddInt32(&attempts, 1)
				q := database.Executor(ctx, db)
				var sum int
				if err := q.QueryRowContext(ctx, `SELECT sum(bal) FROM acct`).Scan(&sum); err != nil {
					return err
				}
				if n <= 2 {
					select {
					case barrier <- struct{}{}:
					case <-time.After(300 * time.Millisecond):
					}
				}
				_, err := q.ExecContext(ctx, `UPDATE acct SET bal = bal + 1 WHERE id = $1`, i)
				return err
			})
			assert.NoError(t, err)
		}(i)
	}
	go func() { <-barrier }()
	wg.Wait()
	var total int
	require.NoError(t, db.QueryRow(`SELECT sum(bal) FROM acct`).Scan(&total))
	assert.Equal(t, 202, total, "both updates applied exactly once")
}

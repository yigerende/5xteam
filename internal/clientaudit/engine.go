package clientaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

type Loader func() (Config, Secrets, error)
type Engine struct {
	DB        *Database
	source    IncrementalSource
	load      Loader
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	runCancel context.CancelFunc
	done      chan struct{}
	wake      chan struct{}
	now       func() time.Time
}

func NewEngine(d *Database, source IncrementalSource, load Loader) *Engine {
	return &Engine{DB: d, source: source, load: load, done: make(chan struct{}), wake: make(chan struct{}, 1), now: time.Now}
}
func (e *Engine) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go e.loop(ctx)
}
func (e *Engine) Close() {
	if e.cancel != nil {
		e.cancel()
		<-e.done
	}
	_ = e.DB.Close()
}
func (e *Engine) Trigger() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return false
	}
	select {
	case e.wake <- struct{}{}:
		return true
	default:
		return false
	}
}
func (e *Engine) Status(c Config) (Status, error) {
	v, err := e.DB.State(c.SourceID())
	e.mu.Lock()
	v.Running = e.running
	e.mu.Unlock()
	return v, err
}
func (e *Engine) loop(ctx context.Context) {
	defer close(e.done)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	next := e.now().Add(time.Minute)
	for {
		manual := false
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
			manual = true
		case <-timer.C:
		}
		c, s, err := e.load()
		if err == nil && (manual || (c.Enabled && !e.now().Before(next))) {
			e.mu.Lock()
			e.running = true
			e.mu.Unlock()
			runctx, cancel := context.WithTimeoutCause(ctx, time.Duration(c.RoundSeconds)*time.Second, errRoundBudget)
			e.mu.Lock()
			e.runCancel = cancel
			e.mu.Unlock()
			err = e.Run(runctx, c, s)
			cancel()
			e.mu.Lock()
			e.runCancel = nil
			e.running = false
			e.mu.Unlock()
			delay := c.IntervalSeconds
			if err != nil {
				delay = c.RetrySeconds
			}
			next = e.now().Add(time.Duration(delay) * time.Second)
			v, _ := e.DB.State(c.SourceID())
			v.NextRun = next.Unix()
			_ = e.DB.SaveState(v)
		}
		timer.Reset(time.Second)
	}
}

var errRoundBudget = errors.New("sync round budget reached")

var entities = []string{"usage", "billing", "funding", "affiliate", "payment", "audit"}

func (e *Engine) Run(ctx context.Context, c Config, secrets Secrets) (err error) {
	if err = c.Validate(); err != nil {
		return err
	}
	if c.DBHost == "" {
		return errors.New("请先配置客户端数据库只读连接")
	}
	source := c.SourceID()
	status, err := e.DB.State(source)
	if err != nil {
		return err
	}
	now := e.now()
	status.LastAttempt = now.Unix()
	status.Error = ""
	status.Segments = 0
	if status.Start == 0 {
		status.Start = dayStart(now.Unix()) - int64(c.HistoryDays-1)*86400
	}
	// Freeze a cycle's end across rounds/restarts. Finished streams must not keep
	// rewinding while another stream catches up, even with a one-batch budget.
	if status.Target <= status.Through || status.Target == 0 {
		status.Target = now.Add(-time.Duration(c.SettlementSeconds) * time.Second).Unix()
	}
	if status.Start > status.Target {
		status.Start = dayStart(status.Target)
	}
	target := time.Unix(status.Target, 0)
	rowsRead := 0
	status.Coverage = "backfilling"
	defer func() {
		// Expiring the configured per-round budget is a normal checkpoint.
		// database/sql may surface ErrTxDone while rolling back the unfinished
		// batch; committed rows and their cursor remain atomic.
		if errors.Is(context.Cause(ctx), errRoundBudget) {
			err = nil
		}
		if err != nil {
			status.Error = err.Error()
		} else {
			status.LastSuccess = e.now().Unix()
		}
		status.Records += int64(rowsRead)
		if x := e.DB.SaveState(status); err == nil && x != nil {
			err = x
		}
		_, _ = e.DB.db.Exec("INSERT INTO audit_runs(source,started,finished,rows_count,message) VALUES(?,?,?,?,?)", source, status.LastAttempt, e.now().Unix(), rowsRead, status.Error)
	}()
	cursors := map[string]Cursor{}
	for _, entity := range entities {
		cur, x := e.DB.Cursor(source, entity)
		if x != nil {
			return x
		}
		if cur.Until != status.Target {
			// A pending cursor belongs to an interrupted cycle and is resumed as-is.
			if cur.Through > 0 {
				lo := max(status.Start, cur.Through-int64(c.OverlapMinutes)*60)
				cur = Cursor{At: time.Unix(lo, 0).Format(time.RFC3339Nano)}
			}
			cur.Until = status.Target
		}
		if cur.At == "" {
			cur.At = time.Unix(status.Start, 0).Format(time.RFC3339Nano)
		}
		cursors[entity] = cur
	}
	// Refresh small tables before long history reads; an incomplete history never
	// makes a snapshot eligible for a 'balanced' verdict.
	currentCtx, cancel := context.WithTimeout(ctx, time.Duration(c.QuerySeconds+2)*time.Second)
	current, at, x := e.source.Current(currentCtx, c, secrets)
	cancel()
	if x != nil {
		return x
	}
	if x = e.DB.BackupBalances(ctx, c, current, at); x != nil {
		return x
	}
	if x = e.DB.SaveCurrent(ctx, source, current, at, 0); x != nil {
		return x
	}
	skipped := 0
	for status.Segments < c.MaxBatches && skipped < len(entities) {
		idx := status.CycleIndex % len(entities)
		entity := entities[idx]
		status.CycleIndex = (idx + 1) % len(entities)
		cur := cursors[entity]
		if cur.Through >= status.Target {
			skipped++
			continue
		}
		skipped = 0
		if x := ctx.Err(); x != nil {
			return x
		}
		from, x := time.Parse(time.RFC3339Nano, cur.At)
		if x != nil {
			return x
		}
		segmentEnd := from.Add(time.Duration(c.SegmentMinutes) * time.Minute)
		if segmentEnd.After(target) {
			segmentEnd = target
		}
		queryCtx, cancel := context.WithTimeout(ctx, time.Duration(c.QuerySeconds+2)*time.Second)
		b, x := e.source.Read(queryCtx, c, secrets, entity, cur, segmentEnd)
		cancel()
		if x != nil {
			return x
		}
		b.Next.Until = status.Target
		if !b.Complete || segmentEnd.Before(target) {
			b.Next.Through = 0
		}
		if x = e.DB.SaveBatch(ctx, source, entity, b); x != nil {
			return x
		}
		cursors[entity] = b.Next
		rowsRead += len(b.Rows)
		status.Segments++
		// Persist scheduler fairness as well as the atomic per-table cursor.
		if x = e.DB.SaveState(status); x != nil {
			return x
		}
		if c.PauseMS > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(c.PauseMS) * time.Millisecond):
			}
		}
	}
	caught := true
	for _, entity := range entities {
		if cursors[entity].Through < status.Target {
			caught = false
		}
	}
	if caught {
		// Use a fresh independent source snapshot after every stream has caught up.
		currentCtx, cancel = context.WithTimeout(ctx, time.Duration(c.QuerySeconds+2)*time.Second)
		current, at, x = e.source.Current(currentCtx, c, secrets)
		cancel()
		if x != nil {
			return x
		}
		if x = e.DB.SaveCurrent(ctx, source, current, at, status.Target); x != nil {
			return x
		}
		if x = e.verifyCandidates(ctx, c, secrets, status.Target); x != nil {
			return x
		}
		status.Through = status.Target
		status.Coverage = "complete"
		if x = e.DB.Prune(ctx, c, source, now.Unix()); x != nil {
			return x
		}
	}
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runCancel != nil {
		e.runCancel()
	}
}

// Backfill only touches the local cursors. It cannot change a source record.
func (e *Engine) Backfill(c Config, start int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running || len(e.wake) > 0 {
		return errors.New("请等待当前同步完成或停止本轮后再补录")
	}
	if start < dayStart(e.now().Unix())-int64(c.DetailDays-1)*86400 || start > dayStart(e.now().Unix()) {
		return errors.New("补录起点须在明细保留期内且不晚于今天")
	}
	source := c.SourceID()
	var pruned int64
	x := e.DB.db.QueryRow("SELECT before FROM audit_pruning WHERE source=?", source).Scan(&pruned)
	if x != nil && !errors.Is(x, sql.ErrNoRows) {
		return x
	}
	if start < pruned {
		return errors.New("该范围的明细已按保留策略清理，不能直接重扫；请使用保留范围内的日期")
	}
	v, err := e.DB.State(source)
	if err != nil {
		return err
	}
	if v.Start > 0 && start > v.Start {
		start = v.Start
	}
	// Don't reread already pruned rows: daily totals are intentionally retained.
	if start < dayStart(e.now().Unix())-int64(c.DetailDays-1)*86400 {
		return errors.New("已有历史早于明细保留期，不能重扫已清理的明细")
	}
	tx, err := e.DB.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{"DELETE FROM audit_cursors WHERE source=?", "DELETE FROM audit_snapshots WHERE source=?"} {
		if _, err = tx.Exec(q, source); err != nil {
			return err
		}
	}
	v.Start = start
	v.Through = 0
	v.Target = 0
	v.Coverage = "backfilling"
	v.Error = ""
	v.CycleIndex = 0
	raw, _ := json.Marshal(v)
	if _, err = tx.Exec("INSERT INTO audit_state VALUES(?,?) ON CONFLICT(source) DO UPDATE SET payload=excluded.payload", source, string(raw)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	e.wake <- struct{}{}
	return nil
}

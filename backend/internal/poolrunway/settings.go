package poolrunway

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

var (
	ErrInvalidGroup   = errors.New("invalid monitoring group")
	ErrConfigConflict = errors.New("monitoring configuration changed")
	ErrCollectorBusy  = errors.New("monitoring collector busy")
	errGroupMissing   = errors.New("group_missing_or_ambiguous")
)

const collectorLockID = int64(871356246)

type Config struct {
	GroupID        int64   `json:"group_id"`
	Revision       int64   `json:"revision"`
	Source         string  `json:"source"`
	EffectiveGroup Group   `json:"effective_group"`
	Groups         []Group `json:"groups"`
}

// Configuration and group choices contain no accounts or credentials.
func (w *Worker) Config(parent context.Context) (Config, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	c := Config{Source: "default", Groups: []Group{}}
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(group_id,0),revision FROM pool_runway_settings WHERE id=1`).Scan(&c.GroupID, &c.Revision); err != nil {
		return c, err
	}
	if c.GroupID > 0 {
		c.Source = "page"
	} else if os.Getenv("POOL_RUNWAY_GROUP_ID") != "" || os.Getenv("POOL_RUNWAY_GROUP_NAME") != "" {
		c.Source = "environment"
	}
	c.EffectiveGroup, err = w.group(ctx, tx)
	if err != nil && !errors.Is(err, errGroupMissing) {
		return c, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name FROM groups WHERE deleted_at IS NULL AND platform='openai' ORDER BY name,id`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var g Group
		if err = rows.Scan(&g.ID, &g.Name); err != nil {
			rows.Close()
			return c, err
		}
		c.Groups = append(c.Groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// Configure writes only the monitoring singleton. It does not collect samples,
// update accounts/group membership, or invoke quota probes.
func (w *Worker) Configure(parent context.Context, groupID, revision int64) error {
	if groupID <= 0 || revision < 0 {
		return ErrInvalidGroup
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, collectorLockID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrCollectorBusy
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM groups WHERE id=$1 AND platform='openai' AND deleted_at IS NULL FOR SHARE`, groupID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidGroup
	}
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE pool_runway_settings SET group_id=$1,revision=revision+1,updated_at=NOW() WHERE id=1 AND revision=$2`, groupID, revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConfigConflict
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	w.mu.Lock()
	w.failed = false
	w.mu.Unlock()
	return nil
}

package repository

import (
	"context"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestGroupPresetCandidatesIncludeDisabledAndExcludeIneligible(t *testing.T) {
	var query string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &query}))
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := &groupRepository{client: client, sql: db}
	mock.ExpectQuery("candidates").WithArgs(int64(43), "mode1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
	ids, err := repo.ListAntiDegradePresetCandidates(context.Background(), 43, "mode1")
	require.NoError(t, err)
	require.Equal(t, []int64{9}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Contains(t, query, "{anti_degrade,enabled}")
	require.Contains(t, query, "IS DISTINCT FROM 'true'")
	require.Contains(t, query, "a.type IN ('oauth', 'setup-token')")
	require.Contains(t, query, "a.parent_account_id IS NULL")
	require.Contains(t, query, "a.platform = 'openai'")
	require.Contains(t, query, "'proxy_mode'")
	require.True(t, strings.Contains(query, "a.deleted_at IS NULL"))
}

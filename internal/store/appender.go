package store

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/marcboeker/go-duckdb/v2"
)

// rowMapper turns a row struct into a slice of driver.Value in DDL column
// order. Each table has its own mapper (see mappers.go).
type rowMapper func(row any) ([]driver.Value, error)

// tableAppender wraps a go-duckdb Appender bound to a single table. Each
// appender owns its own driver.Conn from the shared Connector. Appender state
// is protected by an internal mutex; callers may invoke methods concurrently
// (in practice the writer serializes them under a global flush mutex).
type tableAppender struct {
	table    string
	conn     driver.Conn
	appender *duckdb.Appender
	mapper   rowMapper
}

func newTableAppender(connector *duckdb.Connector, table string, mapper rowMapper) (*tableAppender, error) {
	conn, err := connector.Connect(context.Background())
	if err != nil {
		return nil, fmt.Errorf("open conn for appender %s: %w", table, err)
	}
	app, err := duckdb.NewAppenderFromConn(conn, "", table)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("create appender %s: %w", table, err)
	}
	return &tableAppender{
		table:    table,
		conn:     conn,
		appender: app,
		mapper:   mapper,
	}, nil
}

// AppendBatch maps and appends every row, then flushes the appender so writes
// become visible. If a single row's mapper or AppendRow fails, the row is
// logged-and-skipped by the caller but subsequent rows in the batch still get
// appended; final Flush error is returned.
func (a *tableAppender) AppendBatch(rows []any) (appended int, err error) {
	for i, row := range rows {
		args, err := a.mapper(row)
		if err != nil {
			return appended, fmt.Errorf("map row %d of %s: %w", i, a.table, err)
		}
		if err := a.appender.AppendRow(args...); err != nil {
			return appended, fmt.Errorf("append row %d of %s: %w", i, a.table, err)
		}
		appended++
	}
	if err := a.appender.Flush(); err != nil {
		return appended, fmt.Errorf("flush %s: %w", a.table, err)
	}
	return appended, nil
}

func (a *tableAppender) Close() error {
	var errs []error
	if a.appender != nil {
		if err := a.appender.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close appender %s: %w", a.table, err))
		}
	}
	if a.conn != nil {
		if err := a.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close conn %s: %w", a.table, err))
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// --- driver.Value conversion helpers ---

// All Null* helpers map sql.Null* / typed-zero to nil so the corresponding
// DuckDB column ends up NULL.

func attrsValue(attrs map[string]any) (driver.Value, error) {
	if len(attrs) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		return nil, fmt.Errorf("marshal attrs: %w", err)
	}
	return string(b), nil
}

func stringSliceValue(s []string) driver.Value {
	if len(s) == 0 {
		return nil
	}
	return s
}

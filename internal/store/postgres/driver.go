package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"
)

// rebindingDriver adapts the shared SQL implementation's portable question
// mark parameters to PostgreSQL's positional parameters at the driver edge.
// Keeping this here means transactions and dynamically assembled filters use
// exactly the same queries and behavior in both backends.
type rebindingDriver struct{ base driver.Driver }

func (d rebindingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &rebindingConn{Conn: c}, nil
}

type rebindingConn struct{ driver.Conn }

func (c *rebindingConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(rebind(query))
}

func (c *rebindingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, rebind(query))
	}
	return c.Prepare(query)
}

func (c *rebindingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *rebindingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *rebindingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bc, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bc.BeginTx(ctx, opts)
	}
	return nil, errors.New("postgres driver does not implement context-aware transactions")
}

func (c *rebindingConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *rebindingConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *rebindingConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *rebindingConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

// rebind replaces parameters outside quoted SQL strings and identifiers. The
// shared runtime queries do not use PostgreSQL dollar-quoted strings or
// question marks inside comments.
func rebind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var out strings.Builder
	out.Grow(len(query) + 8)
	parameter := 1
	var quote byte
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if quote != 0 {
			out.WriteByte(ch)
			if ch == quote {
				if i+1 < len(query) && query[i+1] == quote {
					out.WriteByte(query[i+1])
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			out.WriteByte(ch)
		case '?':
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(parameter))
			parameter++
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
}

// Package sqlguard validates SQL that comes from the agent before it is sent to the database.
// It works on the PostgreSQL parse tree, not on text
package sqlguard

import (
	"errors"
	"fmt"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// MaxQueryBytes limits input size before parsing
const MaxQueryBytes = 32 << 10

// Functions with side effects or access outside the query
// Plain EXPLAIN does not execute them, but EXPLAIN ANALYZE will, so the same guard is used for both
var deniedFuncs = map[string]bool{
	"set_config":           true,
	"pg_sleep":             true,
	"pg_sleep_for":         true,
	"pg_sleep_until":       true,
	"pg_terminate_backend": true,
	"pg_cancel_backend":    true,
	"pg_current_xact_id":   true,
	"txid_current":         true,
	"pg_reload_conf":       true,
	"pg_notify":            true,
	"nextval":              true,
	"setval":               true,
	"pg_read_file":         true,
	"pg_read_binary_file":  true,
	"pg_ls_dir":            true,
	"pg_stat_file":         true,
}

var deniedPrefixes = []string{
	"pg_advisory_",
	"pg_try_advisory_",
	"lo_",
	"dblink",
	"hypopg",
	"pg_stat_reset",
	"pg_stat_statements_reset",
}

// CheckSelect accepts exactly one read-only SELECT (WITH ... SELECT allowed)
func CheckSelect(sql string) error {
	if len(sql) > MaxQueryBytes {
		return fmt.Errorf("query is too long: %d bytes, limit is %d", len(sql), MaxQueryBytes)
	}
	tree, err := pg_query.Parse(sql)
	if err != nil {
		return fmt.Errorf("parse query: %w", err)
	}
	if n := len(tree.GetStmts()); n != 1 {
		return fmt.Errorf("expected exactly one statement, got %d", n)
	}
	stmt := tree.GetStmts()[0].GetStmt()
	if stmt.GetSelectStmt() == nil {
		return errors.New("only SELECT statements are allowed")
	}
	return walk(stmt.ProtoReflect(), checkNode)
}

// CheckExplain verifies the final statement that is sent to the server:
// exactly one EXPLAIN without ANALYZE around a query that passes the SELECT rules
func CheckExplain(sql string) error {
	tree, err := pg_query.Parse(sql)
	if err != nil {
		return fmt.Errorf("parse explain: %w", err)
	}
	if n := len(tree.GetStmts()); n != 1 {
		return fmt.Errorf("expected exactly one statement, got %d", n)
	}
	explain := tree.GetStmts()[0].GetStmt().GetExplainStmt()
	if explain == nil {
		return errors.New("expected an EXPLAIN statement")
	}
	for _, opt := range explain.GetOptions() {
		if strings.EqualFold(opt.GetDefElem().GetDefname(), "analyze") {
			return errors.New("EXPLAIN ANALYZE is not allowed here")
		}
	}
	inner := explain.GetQuery()
	if inner.GetSelectStmt() == nil {
		return errors.New("only SELECT statements can be explained")
	}
	return walk(inner.ProtoReflect(), checkNode)
}

func checkNode(m protoreflect.Message) error {
	switch n := m.Interface().(type) {
	case *pg_query.InsertStmt, *pg_query.UpdateStmt, *pg_query.DeleteStmt, *pg_query.MergeStmt:
		return errors.New("data-modifying statements are not allowed, including inside WITH")
	case *pg_query.IntoClause:
		return errors.New("SELECT INTO is not allowed")
	case *pg_query.LockingClause:
		return errors.New("row locking (FOR UPDATE / FOR SHARE) is not allowed")
	case *pg_query.ParamRef:
		return errors.New("parameter placeholders like $1 are not supported, substitute literal values")
	case *pg_query.FuncCall:
		if name := funcName(n); isDenied(name) {
			return fmt.Errorf("function %s is not allowed", name)
		}
	}
	return nil
}

func funcName(f *pg_query.FuncCall) string {
	parts := f.GetFuncname()
	if len(parts) == 0 {
		return ""
	}
	// The last part is the function name; "pg_catalog.set_config" ends with "set_config"
	return parts[len(parts)-1].GetString_().GetSval()
}

func isDenied(name string) bool {
	if deniedFuncs[name] {
		return true
	}
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// walk visits every protobuf message in the parse tree, depth first
func walk(m protoreflect.Message, visit func(protoreflect.Message) error) error {
	if err := visit(m); err != nil {
		return err
	}
	var err error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Message() == nil {
			return true // scalar field
		}
		if fd.IsList() {
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				if err = walk(list.Get(i).Message(), visit); err != nil {
					return false
				}
			}
			return true
		}
		err = walk(v.Message(), visit)
		return err == nil
	})
	return err
}

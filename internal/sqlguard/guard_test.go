package sqlguard

import "testing"

func TestCheckSelect(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		// allowed
		{"simple select", "SELECT * FROM orders WHERE user_id = 42", false},
		{"cte", "WITH t AS (SELECT id FROM users) SELECT count(*) FROM t", false},
		{"join and subquery", "SELECT o.id FROM orders o JOIN (SELECT id FROM users WHERE country = 'DE') u ON u.id = o.user_id", false},
		{"harmless function", "SELECT lower(email) FROM users WHERE created_at > now() - interval '1 day'", false},
		{"trailing semicolon", "SELECT 1;", false},
		{"string literal looks like delete", "SELECT 'delete from orders'", false},

		// rejected: statement shape
		{"two statements", "SELECT 1; SELECT 2", true},
		{"commit escape (security #34)", "COMMIT; SET statement_timeout = 0; SELECT 1", true},
		{"delete", "DELETE FROM orders WHERE id = 1", true},
		{"set", "SET statement_timeout = 0", true},
		{"explain from agent", "EXPLAIN SELECT 1", true},
		{"data-modifying cte", "WITH d AS (DELETE FROM orders WHERE id = 1 RETURNING *) SELECT * FROM d", true},
		{"select into", "SELECT * INTO new_orders FROM orders", true},
		{"for update", "SELECT * FROM orders LIMIT 1 FOR UPDATE", true},
		{"parameter placeholder", "SELECT * FROM orders WHERE user_id = $1", true},
		{"syntax error", "SELEC 1", true},

		// rejected: functions (security #13, #14, #21, #23, #26, #30)
		{"set_config", "SELECT set_config('statement_timeout', '0', false)", true},
		{"set_config uppercase", "SELECT SET_CONFIG('statement_timeout', '0', false)", true},
		{"set_config qualified", "SELECT pg_catalog.set_config('statement_timeout', '0', false)", true},
		{"set_config in cte", "WITH s AS (SELECT set_config('statement_timeout', '0', false)) SELECT * FROM s", true},
		{"pg_sleep in subquery", "SELECT * FROM (SELECT pg_sleep(10)) s", true},
		{"advisory lock", "SELECT pg_advisory_lock(42)", true},
		{"terminate backend", "SELECT pg_terminate_backend(pid) FROM pg_stat_activity", true},
		{"xact id", "SELECT pg_current_xact_id()", true},
		{"read file", "SELECT pg_read_file('/etc/passwd')", true},
		{"large object", "SELECT lo_import('/etc/passwd')", true},
		{"hypopg from agent", "SELECT * FROM hypopg_create_index('CREATE INDEX ON orders (total)')", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckSelect(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckSelect(%q) error = %v, wantErr %v", tt.sql, err, tt.wantErr)
			}
		})
	}
}

func TestCheckExplain(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		{"plain explain", "EXPLAIN (FORMAT JSON) SELECT 1", false},
		{"explain analyze", "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1", true},
		{"explain delete", "EXPLAIN DELETE FROM orders", true},
		{"two statements", "EXPLAIN SELECT 1; SELECT 2", true},
		{"denied function inside", "EXPLAIN SELECT pg_sleep(1)", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckExplain(tt.sql)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckExplain(%q) error = %v, wantErr %v", tt.sql, err, tt.wantErr)
			}
		})
	}
}

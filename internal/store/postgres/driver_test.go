package postgres

import "testing"

func TestRebind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "parameters", in: "SELECT ? WHERE a = ?", want: "SELECT $1 WHERE a = $2"},
		{name: "single quoted", in: "SELECT '?' WHERE a = ?", want: "SELECT '?' WHERE a = $1"},
		{name: "escaped quote", in: "SELECT 'it''s ?' WHERE a = ?", want: "SELECT 'it''s ?' WHERE a = $1"},
		{name: "identifier", in: `SELECT "?" WHERE a = ?`, want: `SELECT "?" WHERE a = $1`},
		{name: "none", in: "SELECT 1", want: "SELECT 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := rebind(test.in); got != test.want {
				t.Fatalf("rebind(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestValidateDSN(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"postgres://user:pass@db.example/spendlease",
		"postgresql://db.example/spendlease",
	} {
		if err := validateDSN(dsn); err != nil {
			t.Errorf("validateDSN(%q): %v", dsn, err)
		}
	}
	for _, dsn := range []string{"spendlease.db", "mysql://db.example/name", "postgres:///name"} {
		if err := validateDSN(dsn); err == nil {
			t.Errorf("validateDSN(%q) unexpectedly succeeded", dsn)
		}
	}
}

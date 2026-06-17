package db

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestConfigDSN verifies that only explicitly-set fields end up in the DSN,
// so that pgx can fall back to PG* environment variables for the rest.
func TestConfigDSN(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "empty config defers everything to the environment",
			cfg:  Config{},
			want: "",
		},
		{
			name: "full config",
			cfg: Config{
				Host: "db1", Port: "5433", User: "alice",
				Password: "secret", Database: "app", SSLMode: "require",
			},
			want: "host=db1 port=5433 user=alice password=secret dbname=app sslmode=require",
		},
		{
			name: "partial config only includes set fields",
			cfg:  Config{Database: "app", User: "alice"},
			want: "user=alice dbname=app",
		},
		{
			name: "password with special characters is quoted",
			cfg:  Config{Password: `p@ss w'ord\100%`},
			want: `password='p@ss w\'ord\\100%'`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.DSN(); got != tc.want {
				t.Errorf("DSN() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestQuoteDSNValue covers the libpq key/value quoting rules.
func TestQuoteDSNValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"with space", "'with space'"},
		{"quo'te", `'quo\'te'`},
		{`back\slash`, `'back\\slash'`},
		{"", "''"},
		{"p@ss:word/100%", "p@ss:word/100%"}, // URL metacharacters need no quoting in key/value form
	}

	for _, tc := range cases {
		if got := quoteDSNValue(tc.in); got != tc.want {
			t.Errorf("quoteDSNValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDSNParseable verifies that a DSN with awkward values round-trips
// through pgx's parser (the old URL-based builder broke on these).
func TestDSNParseable(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: "5432", User: "alice",
		Password: `p@ss w'ord\100%`, Database: "app", SSLMode: "disable",
	}

	parsed, err := pgx.ParseConfig(cfg.DSN())
	if err != nil {
		t.Fatalf("pgx failed to parse DSN: %v", err)
	}
	if parsed.Password != `p@ss w'ord\100%` {
		t.Errorf("password mangled by DSN round-trip: got %q", parsed.Password)
	}
	if parsed.Database != "app" {
		t.Errorf("database mangled by DSN round-trip: got %q", parsed.Database)
	}
}

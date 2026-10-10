package schema_test

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"

	"github.com/D1-2004/eino-employee-loop/schema"
)

// statements returns the SQL statements of a file without comments.
func statements(t *testing.T, name string) []string {
	t.Helper()
	raw, err := schema.Read(name)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		lines = append(lines, line)
	}
	var out []string
	for _, st := range strings.Split(strings.Join(lines, "\n"), ";") {
		if st = strings.TrimSpace(st); st != "" {
			out = append(out, st)
		}
	}
	return out
}

var (
	tableRe = regexp.MustCompile(`(?s)^CREATE TABLE IF NOT EXISTS (\w+) \((.*)\)$`)
	indexRe = regexp.MustCompile(`^CREATE (UNIQUE )?INDEX (CONCURRENTLY )?IF NOT EXISTS (\w+)\s+ON (\w+) \(([^)]*)\)(.*)$`)
)

type layout struct {
	tables  map[string][]string // table -> column names
	indexes map[string]string   // index -> normalized definition
}

func parseLayout(t *testing.T, name string) layout {
	t.Helper()
	l := layout{tables: map[string][]string{}, indexes: map[string]string{}}
	for _, st := range statements(t, name) {
		flat := strings.Join(strings.Fields(st), " ")
		if m := tableRe.FindStringSubmatch(st); m != nil {
			for _, line := range strings.Split(m[2], "\n") {
				f := strings.Fields(strings.TrimSpace(line))
				if len(f) == 0 || f[0] == "PRIMARY" {
					continue
				}
				l.tables[m[1]] = append(l.tables[m[1]], f[0])
			}
			continue
		}
		if m := indexRe.FindStringSubmatch(flat); m != nil {
			l.indexes[m[3]] = m[1] + m[4] + "(" + m[5] + ")" + m[6]
			continue
		}
		t.Errorf("%s: unexpected statement: %s", name, flat)
	}
	return l
}

// services lists the owners of reference DDL: each service has its own
// <service>.postgres.sql and <service>.sqlite.sql.
var services = []string{"loop", "taskmgr"}

func ddlFiles() []string {
	var out []string
	for _, svc := range services {
		out = append(out, svc+".postgres.sql", svc+".sqlite.sql")
	}
	return out
}

func TestEachServiceOwnsItsTables(t *testing.T) {
	owner := map[string]string{}
	for _, svc := range services {
		for table := range parseLayout(t, svc+".postgres.sql").tables {
			if prev, ok := owner[table]; ok {
				t.Errorf("table %s is defined by %s and %s", table, prev, svc)
			}
			owner[table] = svc
			if (svc == "taskmgr") != strings.HasPrefix(table, "task") {
				t.Errorf("table %s does not belong to %s", table, svc)
			}
		}
	}
	if len(owner) == 0 {
		t.Fatal("no tables found")
	}
}

func TestDDLHasNoForeignKeysOrCascades(t *testing.T) {
	banned := regexp.MustCompile(`(?i)\b(REFERENCES|FOREIGN\s+KEY|CASCADE|CREATE\s+TRIGGER)\b`)
	for _, name := range ddlFiles() {
		for _, st := range statements(t, name) {
			if banned.MatchString(st) {
				t.Errorf("%s: %s", name, st)
			}
		}
	}
}

func TestPostgresIndexesAreConcurrentSingleStatements(t *testing.T) {
	for _, svc := range services {
		n := 0
		for _, st := range statements(t, svc+".postgres.sql") {
			flat := strings.Join(strings.Fields(st), " ")
			if !strings.Contains(flat, " INDEX ") {
				continue
			}
			n++
			m := indexRe.FindStringSubmatch(flat)
			if m == nil || m[2] == "" {
				t.Errorf("%s: index is not built CONCURRENTLY: %s", svc, flat)
			}
		}
		if n == 0 {
			t.Fatalf("%s: no index statements found", svc)
		}
	}
}

func TestPostgresAndSQLiteLayoutsMatch(t *testing.T) {
	for _, svc := range services {
		t.Run(svc, func(t *testing.T) { layoutsMatch(t, svc) })
	}
}

func layoutsMatch(t *testing.T, svc string) {
	pg, lite := parseLayout(t, svc+".postgres.sql"), parseLayout(t, svc+".sqlite.sql")
	names := func(m map[string][]string) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if a, b := names(pg.tables), names(lite.tables); strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("tables differ:\npostgres %v\nsqlite   %v", a, b)
	}
	for table, cols := range pg.tables {
		if strings.Join(cols, ",") != strings.Join(lite.tables[table], ",") {
			t.Errorf("%s columns differ:\npostgres %v\nsqlite   %v", table, cols, lite.tables[table])
		}
	}
	if len(pg.indexes) != len(lite.indexes) {
		t.Errorf("index counts differ: %d vs %d", len(pg.indexes), len(lite.indexes))
	}
	for name, def := range pg.indexes {
		if lite.indexes[name] != def {
			t.Errorf("index %s differs:\npostgres %s\nsqlite   %s", name, def, lite.indexes[name])
		}
	}
}

func TestSQLiteDDLAppliesTwiceAndEnforcesUniqueness(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for pass := 0; pass < 2; pass++ {
		for _, svc := range services {
			for _, st := range statements(t, svc+".sqlite.sql") {
				if _, err := db.Exec(st); err != nil {
					t.Fatalf("pass %d: %s: %v\n%s", pass, svc, err, st)
				}
			}
		}
	}
	window := `INSERT INTO session_windows (id, employee, scene_id, session_kind, session_key, lane, state, opened_at, due_at, updated_at)
		VALUES (?, 'e1', 's1', 'observation', '', 'background', ?, '2026-10-11T09:00:00.000Z', '2026-10-11T09:00:12.000Z', '2026-10-11T09:00:00.000Z')`
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(window, "w1", "open")
	mustExec(window, "w2", "ready")
	if _, err := db.Exec(window, "w3", "open"); err == nil {
		t.Error("a second open window of one session was accepted")
	}
	mustExec(window, "w4", "running")
	if _, err := db.Exec(window, "w5", "running"); err == nil {
		t.Error("a second running window of one session was accepted")
	}
	receipt := `INSERT INTO event_receipts (id, employee, source, event_id, type, class, mode, lane, fingerprint, event, admitted_at)
		VALUES (?, 'e1', 'chat.im', 'm1', 'message.addressed', 'business', 'triggered', 'foreground', 'fp', '{}', '2026-10-11T09:00:00.000Z')`
	mustExec(receipt, "r1")
	if _, err := db.Exec(receipt, "r2"); err == nil {
		t.Error("a second receipt for one (employee, source, event id) was accepted")
	}
	if _, err := db.Exec(`INSERT INTO outbox_deliveries (employee, id, scene_id, scene, session_kind, window_id, text, state, created_at, updated_at)
		VALUES ('e1', 'w1/reply', 's1', '{}', 'interactive', 'w1', 'hi', 'lost', 'x', 'x')`); err == nil {
		t.Error("an unknown delivery state was accepted")
	}
}

// TestPostgresDDLApplies runs every service's PostgreSQL DDL against a scratch database when
// EEL_TEST_POSTGRES_URL names one, for example
// postgres://postgres:postgres@localhost:5432/eel_schema_test.
func TestPostgresDDLApplies(t *testing.T) {
	url := os.Getenv("EEL_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("EEL_TEST_POSTGRES_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for pass := 0; pass < 2; pass++ {
		for _, svc := range services {
			for _, st := range statements(t, svc+".postgres.sql") {
				if _, err := conn.Exec(ctx, st); err != nil {
					t.Fatalf("pass %d: %s: %v\n%s", pass, svc, err, st)
				}
			}
		}
	}
}

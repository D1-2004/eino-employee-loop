// Package schema embeds the wire and storage schemas of the Employee Loop:
//
//   - *.schema.json: JSON Schemas (draft 2020-12) of the values that cross
//     the service boundaries (events, deliveries, Task Manager requests,
//     receipts and views, errors) and of the catalog entries (event types,
//     tool specs). Shared definitions live in defs.schema.json and are
//     referenced by relative $ref.
//   - <service>.postgres.sql and <service>.sqlite.sql: reference DDL, one
//     pair per service that owns the tables. loop.* backs the Employee
//     Loop's storage ports (EventLog, SessionQueue, Journal, Outbox,
//     MemoryStore, Conversation); taskmgr.* is a durable Task Manager. The
//     module ships in-memory implementations only (docs/storage.md).
//
// Tests keep the JSON Schemas in step with the Go JSON tags of the contract
// types and validate sample instances against them.
package schema

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.schema.json *.sql
var files embed.FS

// FS returns the embedded schema files.
func FS() fs.FS { return files }

// JSONSchemas lists the embedded JSON Schema file names, sorted.
func JSONSchemas() []string {
	entries, _ := fs.ReadDir(files, ".")
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".schema.json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Read returns one embedded file.
func Read(name string) ([]byte, error) { return files.ReadFile(name) }

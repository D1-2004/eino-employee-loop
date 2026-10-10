// Command catalog-doc renders the standard event-type and tool catalogs as
// a Markdown document in one locale:
//
//	go run ./cmd/catalog-doc -locale en -o docs/catalog.md
//	go run ./cmd/catalog-doc -locale zh-CN -o docs/catalog.zh-CN.md
//
// The tables come from contract/catalog/*.json and the i18n packs only (the
// loop's contract pack and the catalogdoc pack);
// internal/catalogdoc tests that the committed documents match.
package main

import (
	"flag"
	"os"

	"github.com/D1-2004/eino-employee-loop/i18n"
	"github.com/D1-2004/eino-employee-loop/internal/catalogdoc"
	"github.com/D1-2004/eino-employee-loop/internal/cli"
)

func main() {
	con := cli.NewConsole(catalogdoc.LoadMessages)
	locale := flag.String("locale", i18n.Fallback, con.T("catalogdoc.flag.locale"))
	out := flag.String("o", "-", con.T("catalogdoc.flag.out"))
	flag.Parse()

	doc, err := catalogdoc.Standard(*locale)
	if err != nil {
		con.Fail(err)
	}
	if *out == "-" || *out == "" {
		if _, err := os.Stdout.WriteString(doc); err != nil {
			con.Fail(err)
		}
		return
	}
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		con.Fail(err)
	}
}

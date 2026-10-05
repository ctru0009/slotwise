package arch_test

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const mod = "github.com/ctru0009/slotwise/"

var rules = []struct {
	root   string
	forbid []string
}{
	{"internal/domain", []string{mod + "internal/app", mod + "internal/adapters", "net/http", "database/sql", "github.com/jackc/pgx"}},
	{"internal/app", []string{mod + "internal/adapters", "net/http", "github.com/jackc/pgx", "github.com/a-h/templ"}},
	{"internal/adapters/web", []string{mod + "internal/adapters/postgres", "github.com/jackc/pgx"}},
}

func TestDependencyRules(t *testing.T) {
	t.Parallel()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps}
	for _, r := range rules {
		pkgs, err := packages.Load(cfg, mod+r.root+"/...")
		if err != nil {
			t.Fatalf("load %s: %v", r.root, err)
		}
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			if !strings.HasPrefix(p.PkgPath, mod+r.root) {
				return
			}
			for imp := range p.Imports {
				for _, f := range r.forbid {
					if strings.HasPrefix(imp, f) {
						t.Errorf("%s must not import %s", p.PkgPath, imp)
					}
				}
			}
		})
	}
}

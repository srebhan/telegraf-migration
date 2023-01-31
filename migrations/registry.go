package migrations

import (
	"fmt"

	"github.com/influxdata/toml/ast"
)

type PluginMigrationFunc func(*ast.Table) ([]byte, error)

var PluginMigrations = make(map[string]PluginMigrationFunc)

func AddPluginMigration(name string, f PluginMigrationFunc) {
	if _, found := PluginMigrations[name]; found {
		panic(fmt.Errorf("plugin migration function already registered for %q", name))
	}
	PluginMigrations[name] = f
}

func CreateTOMLStruct(category, plugin string) map[string]map[string][]interface{} {
	return map[string]map[string][]interface{}{
		category: {
			plugin: make([]interface{}, 0),
		},
	}
}

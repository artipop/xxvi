package app

import (
	"github.com/artipop/xxvi/internal/hosting"
	"github.com/artipop/xxvi/internal/inbox"
	"github.com/artipop/xxvi/internal/sources"
)

// builtinSources are the sources this application reads with its own code, as
// the catalog lists them beside the plugins. Describing them there is what lets
// the sources form hold no source by name: it is built from the list.
func builtinSources() []sources.Manifest {
	return []sources.Manifest{
		{
			Name: inbox.PluginDemo, Title: "Файл (демо)",
			Fields: []sources.Field{{Key: inbox.ConfigPath, Title: "Файл со списком"}},
		},
		// Made by connecting a project's hosting, where the project says how.
		{Name: hosting.PluginReview, Title: "MR на ревью", Managed: true},
	}
}

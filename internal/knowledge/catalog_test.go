package knowledge

import (
	"io/fs"

	"github.com/jguan/aima/catalog"
)

func catalogFS() fs.FS {
	return catalog.FS
}

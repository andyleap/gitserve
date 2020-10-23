package main

import (
	"fmt"
	"path"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/go-jsonnet"
)

func processJsonnet(tree *object.Tree, path string) (string, error) {
	vm := jsonnet.MakeVM()
	ti := &treeImporter{t: tree}
	vm.Importer(ti)
	ast, _, err := vm.ImportAST("", path)
	if err != nil {
		return "", err
	}
	return vm.Evaluate(ast)
}

type treeImporter struct {
	t        *object.Tree
	prefixes []string
}

// Import fetches data from a given path. It may be relative
// to the file where we do the import. What "relative path"
// means depends on the importer.
//
// It is required that:
// a) for given (importedFrom, importedPath) the same
//    (contents, foundAt) are returned on subsequent calls.
// b) for given foundAt, the contents are always the same
//
// It is recommended that if there are multiple locations that
// need to be probed (e.g. relative + multiple library paths)
// then all results of all attempts will be cached separately,
// both nonexistence and contents of existing ones.
// FileImporter may serve as an example.
//
// Importing the same file multiple times must be a cheap operation
// and shouldn't involve copying the whole file - the same buffer
// should be returned.
func (ti *treeImporter) Import(importedFrom string, importedPath string) (contents jsonnet.Contents, foundAt string, err error) {
	f, err := ti.t.File(importedPath)
	if err == nil {
		data, err := f.Contents()
		return jsonnet.MakeContents(data), importedPath, err
	}
	for _, p := range ti.prefixes {
		ipath := path.Join(p, importedPath)
		f, err := ti.t.File(importedPath)
		if err == nil {
			data, err := f.Contents()
			return jsonnet.MakeContents(data), ipath, err
		}
	}
	return jsonnet.Contents{}, "", fmt.Errorf("Could not find %s", importedPath)
}

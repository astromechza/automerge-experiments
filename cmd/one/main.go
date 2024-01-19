package main

import (
	"fmt"
	"log/slog"

	"github.com/automerge/automerge-go"
	_ "github.com/automerge/automerge-go"
)

// A basic example of creating a doc, changing some things, committing those changes, and then copying those changes
// over to another doc.
func main() {

	// create doc number one
	doc := automerge.New()

	if err := doc.Path("x").Set(1); err != nil {
		panic(err)
	}

	if err := doc.Path("y").Set(map[string]interface{}{"a": "b"}); err != nil {
		panic(err)
	}

	slog.Info(doc.RootMap().GoString())

	if _, err := doc.Commit("hi", automerge.CommitOptions{AllowEmpty: true}); err != nil {
		panic(err)
	}

	changes, err := doc.Changes()
	if err != nil {
		panic(err)
	}
	for i, c := range changes {
		slog.Info(fmt.Sprintf("change %d: %#v", i, c))
	}

	newdoc := automerge.New()

	if err := newdoc.Apply(changes...); err != nil {
		panic(err)
	}

	slog.Info(doc.RootMap().GoString())
	slog.Info(newdoc.RootMap().GoString())
}

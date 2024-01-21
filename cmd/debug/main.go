package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/automerge/automerge-go"
)

func main() {
	if err := mainInner(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func mainInner() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{})))

	flag.Parse()
	if flag.NArg() != 1 {
		return fmt.Errorf("expected one position argument: the file to read")
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		return fmt.Errorf("failed to open input file: %w", err)
	}
	defer f.Close()
	buff, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("failed to read input file: %w", err)
	}
	doc, err := automerge.Load(buff)
	if err != nil {
		return fmt.Errorf("failed to load doc: %w", err)
	}
	buff = nil
	slog.Info("loaded doc", "contents", doc.RootMap().GoString())
	slog.Info("loaded heads", "heads", doc.Heads())

	slog.Info("changes:")

	changes, err := doc.Changes()
	if err != nil {
		return fmt.Errorf("failed to generate changes: %w", err)
	}
	for i, change := range changes {
		slog.Info("change", "i", fmt.Sprintf("%4d", i), "hash", change.Hash(), "actor", change.ActorID(), "dep", change.Dependencies())
	}

	fmt.Println(`digraph "log" {`)
	for _, change := range changes {
		docAt, _ := doc.Fork(change.Hash())
		value, _ := docAt.Path("counter").Counter().Get()
		fmt.Printf("    \"%s\" [label=\"%s %s@%d %d\"]\n", change.Hash(), change.Hash().String()[:8], change.ActorID(), change.ActorSeq(), value)
		for _, hash := range change.Dependencies() {
			fmt.Printf("    \"%s\" -> \"%s\"\n", hash, change.Hash())
		}
	}
	fmt.Println("}")
	return nil
}

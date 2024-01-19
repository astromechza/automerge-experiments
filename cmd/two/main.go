package main

import (
	"log/slog"

	"github.com/automerge/automerge-go"
)

// work out how sync states work
func main() {

	doc := automerge.New()
	ss := automerge.NewSyncState(doc)

	doc2 := automerge.New()
	ss2 := automerge.NewSyncState(doc2)

	// lets start by getting both empty documents in sync
	if err := sync(ss, ss2); err != nil {
		panic(err)
	}

	// no content/changes so no heads
	slog.Info("heads", "h", doc.Heads())
	slog.Info("heads", "h", doc2.Heads())

	// create a change (this will auto commit)
	if err := doc.Path("x").Set(1); err != nil {
		panic(err)
	}
	if err := doc2.Path("y").Set(1); err != nil {
		panic(err)
	}
	if err := doc2.Path("x").Set(2); err != nil {
		panic(err)
	}

	slog.Info(doc.RootMap().GoString())
	slog.Info(doc2.RootMap().GoString())

	// now we have a head
	slog.Info("heads", "h", doc.Heads())
	if c, err := doc.Changes(); err != nil {
		panic(err)
	} else {
		slog.Info("changes", "c", c)
	}

	if err := sync(ss, ss2); err != nil {
		panic(err)
	}

	slog.Info(doc.RootMap().GoString())
	slog.Info(doc2.RootMap().GoString())

}

func sync(ss1 *automerge.SyncState, ss2 *automerge.SyncState) error {
	hadMessages := true
	for hadMessages {
		hadMessages = false

		for {
			if msg, valid := ss1.GenerateMessage(); valid {
				slog.Info("ss1 send to ss2", "m", msg.Heads(), "c", msg.Changes())
				hadMessages = true
				if _, err := ss2.ReceiveMessage(msg.Bytes()); err != nil {
					return err
				}
			} else {
				break
			}
		}

		for {
			if msg, valid := ss2.GenerateMessage(); valid {
				slog.Info("ss2 send to ss1", "m", msg.Heads(), "c", msg.Changes())
				hadMessages = true
				if _, err := ss1.ReceiveMessage(msg.Bytes()); err != nil {
					return err
				}
			} else {
				break
			}
		}
	}
	return nil
}

package pkg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/automerge/automerge-go"
	"github.com/gorilla/websocket"
)

func readAndReceiveMessage(
	conn *websocket.Conn,
	syncState *automerge.SyncState,
) error {
	mt, p, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("failed to read message: %w", err)
	}
	switch mt {
	case websocket.BinaryMessage:
		if _, err := syncState.ReceiveMessage(p); err != nil {
			return fmt.Errorf("failed to receive message: %w", err)
		}
	default:
	}
	return nil
}

func generateAndWriteMessage(
	conn *websocket.Conn,
	syncState *automerge.SyncState,
) (bool, error) {
	if msg, valid := syncState.GenerateMessage(); msg != nil {
		if err := conn.WriteMessage(websocket.BinaryMessage, msg.Bytes()); err != nil {
			return false, fmt.Errorf("failed to write message: %w", err)
		}
		return valid, nil
	}
	return false, nil
}

func Sync(
	ctx context.Context,
	conn *websocket.Conn,
	syncState *automerge.SyncState,
) error {
	slog.Info("syncing")

	wg := new(sync.WaitGroup)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer conn.Close()
		for {
			if err := readAndReceiveMessage(conn, syncState); err != nil {
				slog.Error(err.Error())
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer conn.Close()

		for {
			if ok, err := generateAndWriteMessage(conn, syncState); err != nil {
				slog.Error(err.Error())
				return
			} else if !ok {
				break
			}
		}

		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				for {
					if ok, err := generateAndWriteMessage(conn, syncState); err != nil {
						slog.Error(err.Error())
						return
					} else if !ok {
						break
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()
	return nil
}

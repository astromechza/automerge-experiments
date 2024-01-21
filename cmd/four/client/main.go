package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/automerge/automerge-go"
	"github.com/gorilla/websocket"

	"github.com/astromechza/automerge-experiments/cmd/four/pkg"
)

func main() {
	if err := mainInner(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func mainInner() error {
	addrVar := flag.String("addr", "127.0.0.1:8080", "the address to request on")
	baseUrl, err := url.Parse("http://" + *addrVar)
	if err != nil {
		return err
	}

	var doc *automerge.Doc

	resp, err := http.DefaultClient.Get(baseUrl.JoinPath("stores/default/latest").String())
	if err != nil {
		return fmt.Errorf("failed to get: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:

		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read body from get: %w", err)
		}
		if d, err := automerge.Load(raw); err != nil {
			return fmt.Errorf("failed to load doc: %w", err)
		} else {
			doc = d
			_ = doc.SetActorID(hex.EncodeToString([]byte(fmt.Sprintf("%d", os.Getpid()))))
		}
	default:
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	slog.Info("established base doc", "heads", doc.Heads())
	c := &client{doc: doc, baseUrl: baseUrl}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg := new(sync.WaitGroup)

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.connectAndSyncContinuously(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.incrementRandomlyContinuously(ctx)
	}()

	exit := make(chan os.Signal, 1) // we need to reserve to buffer size 1, so the notifier are not blocked
	signal.Notify(exit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-exit
	slog.Info("Signal caught", "sig", sig)
	cancel()

	wg.Wait()

	tf := filepath.Join(os.TempDir(), doc.ActorID()+".doc")
	if f, err := os.Create(tf); err != nil {
		return err
	} else {
		defer f.Close()
		if _, err := f.Write(doc.Save()); err != nil {
			return err
		}
	}
	slog.Info("dumped", "dump", tf)
	return nil
}

type client struct {
	baseUrl *url.URL
	doc     *automerge.Doc
}

func (c *client) connectAndSyncContinuously(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := c.connectAndSync(ctx); err != nil {
				slog.Error("failed to sync", "err", err)
			} else {
				slog.Info("finished sync")
			}
		case <-ctx.Done():
			slog.Info("stopping scheduled sync")
			return
		}
	}
}

func (c *client) connectAndSync(ctx context.Context) error {
	u := c.baseUrl.JoinPath("stores/default/sync")
	u.Scheme = "ws"
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}
	defer conn.Close()
	syncState := automerge.NewSyncState(c.doc)
	if err := pkg.Sync(ctx, conn, syncState); err != nil {
		return fmt.Errorf("failed to sync: %w", err)
	}
	return nil
}

func (c *client) incrementRandomlyContinuously(ctx context.Context) {
	for {
		t := time.NewTimer(time.Second + time.Second*time.Duration(rand.Intn(5)))
		select {
		case <-t.C:
			if err := c.doc.Path("counter").Counter().Inc(1); err != nil {
				slog.Error("failed to increment counter", "err", err)
			} else {
				value, _ := c.doc.Path("counter").Counter().Get()
				slog.Info("incremented", "heads", c.doc.Heads(), "value", value)
			}
		case <-ctx.Done():
			slog.Info("stopping scheduled increment")
			return
		}
	}
}

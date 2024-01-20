package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/automerge/automerge-go"
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
	docLock := new(sync.Mutex)

	slog.Info("Checking current state", "url", baseUrl.JoinPath("get").String())
	resp, err := http.DefaultClient.Get(baseUrl.JoinPath("get").String())
	if err != nil {
		return fmt.Errorf("failed to get: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Content []byte `json:"content"`
		}

		err := json.NewDecoder(resp.Body).Decode(&out)
		if err != nil {
			return fmt.Errorf("failed to read body from get: %w", err)
		}
		slog.Info("got doc", "doc", base64.StdEncoding.EncodeToString(out.Content))
		if d, err := automerge.Load(out.Content); err != nil {
			return fmt.Errorf("failed to load doc: %w", err)
		} else {
			doc = d
			_ = doc.SetActorID(hex.EncodeToString([]byte(fmt.Sprintf("%d", os.Getpid()))))
		}
	case http.StatusNoContent:
	default:
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if doc == nil {
		slog.Info("no remote state, creating new doc")
		doc = automerge.New()
		_ = doc.SetActorID(hex.EncodeToString([]byte(fmt.Sprintf("%d", os.Getpid()))))
		_, _ = doc.Commit("seed", automerge.CommitOptions{AllowEmpty: true})

		body, _ := json.Marshal(map[string]interface{}{
			"content": doc.Save(),
		})

		slog.Info("Uploading new state", "url", baseUrl.JoinPath("new").String())
		resp, err := http.DefaultClient.Post(baseUrl.JoinPath("new").String(), "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNoContent:
		default:
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
	}

	slog.Info("established base doc", "heads", doc.Heads())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wg := new(sync.WaitGroup)

	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()

		var lastCookie *string
		syncState := automerge.NewSyncState(doc)

		inner := func() bool {
			select {
			case <-t.C:
				docLock.Lock()
				defer docLock.Unlock()
				slog.Info("attempting sync")

				outGoingMessages := make([][]byte, 0)
				if lastCookie != nil {
					for {
						msg, valid := syncState.GenerateMessage()
						if !valid {
							break
						}
						outGoingMessages = append(outGoingMessages, msg.Bytes())
					}
				}

				body, err := json.Marshal(map[string]interface{}{
					"cookie":   lastCookie,
					"messages": outGoingMessages,
				})
				if err != nil {
					slog.Error("failed to encode body", "err", err)
					break
				}

				slog.Info("sending sync request")
				resp, err := http.DefaultClient.Post(baseUrl.JoinPath("sync").String(), "application/json", bytes.NewReader(body))
				if err != nil {
					slog.Error("failed to start sync", "err", err)
					break
				}
				defer resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusOK:
					slog.Info("got sync response")
				default:
					slog.Error("unexpected status code", "code", resp.StatusCode)
					break
				}

				var out struct {
					Cookie   string   `json:"cookie"`
					Messages [][]byte `json:"messages"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
					slog.Error("failed to read sync body", "err", err)
					break
				}

				if out.Cookie != "" {
					lastCookie = &out.Cookie
				}

				for _, message := range out.Messages {
					if _, err := syncState.ReceiveMessage(message); err != nil {
						slog.Error("failed to load sync message", "err", err)
						break
					}
				}

				slog.Info("doc heads", "heads", doc.Heads(), "map", doc.RootMap().GoString())

			case <-ctx.Done():
				slog.Info("stopping scheduled sync")
				return false
			}
			return true
		}

		for inner() {
		}
		slog.Info("stopped scheduled sync")

	}()

	wg.Add(1)

	go func() {
		defer wg.Done()
		inner := func() bool {
			t := time.NewTimer(time.Second + time.Second*time.Duration(rand.Intn(5)))
			select {
			case <-t.C:
				docLock.Lock()
				defer docLock.Unlock()
				if err := doc.Path("counter").Counter().Inc(1); err != nil {
					slog.Error("failed to increment counter", "err", err)
				} else {
					count, _ := doc.Path("counter").Counter().Get()
					slog.Info("counter incremented", "count", count)
				}
				if _, err := doc.Commit("incremented"); err != nil {
					slog.Error("failed to commit doc", "err", err)
				}
			case <-ctx.Done():
				slog.Info("stopping scheduled increment")
				return false
			}
			return true
		}
		for inner() {
		}

	}()

	exit := make(chan os.Signal, 1) // we need to reserve to buffer size 1, so the notifier are not blocked
	signal.Notify(exit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-exit
	slog.Info("Signal caught", "sig", sig)
	cancel()

	wg.Wait()

	return nil
}

package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"

	"github.com/automerge/automerge-go"

	"github.com/astromechza/automerge-experiments/cmd/four/pkg"
	"github.com/astromechza/automerge-experiments/pkg/viz"
)

func main() {
	if err := mainInner(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func mainInner() error {
	addrVar := flag.String("addr", "localhost:8080", "the address to listen on")
	slog.Info("Opening database")
	db, err := sql.Open("sqlite3", "four.sqlite3")
	if err != nil {
		return err
	}
	defer db.Close()
	s := &server{database: db}
	if err := s.init(); err != nil {
		panic(err)
	}

	r := mux.NewRouter()
	r.Use(func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			m := httpsnoop.CaptureMetrics(handler, writer, request)
			slog.Info("handled", "method", request.Method, "url", request.URL, "duration", m.Duration, "status", m.Code)
		})
	})

	r.Methods(http.MethodGet).Path("/stores/{store}/latest").HandlerFunc(s.getStore)
	r.Methods(http.MethodGet).Path("/stores/{store}/sync").HandlerFunc(s.syncStore)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := new(sync.WaitGroup)

	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Second * 5)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.cache.Range(func(storeId, docRaw any) bool {
					newContent := base64.StdEncoding.EncodeToString(docRaw.(*automerge.Doc).Save())
					if res, err := s.database.ExecContext(
						ctx, `UPDATE stores SET content = ? WHERE id = ? AND content != ? `,
						newContent,
						storeId,
						newContent,
					); err != nil {
						slog.Error("failed to backup doc in database", "err", err)
					} else if r, _ := res.RowsAffected(); r > 0 {
						slog.Info("backed up", "store", storeId, "heads", docRaw.(*automerge.Doc).Heads())
					}
					return true
				})
			case <-ctx.Done():
				return
			}
		}
	}()

	httpServer := &http.Server{Addr: *addrVar, Handler: r}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server listen failed", err.Error())
		}
	}()

	exit := make(chan os.Signal, 1) // we need to reserve to buffer size 1, so the notifier are not blocked
	signal.Notify(exit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-exit
	slog.Info("Signal caught", "sig", sig)
	cancel()
	_ = httpServer.Close()

	wg.Wait()

	s.cache.Range(func(storeId, docRaw any) bool {
		doc := docRaw.(*automerge.Doc)
		tf := filepath.Join(os.TempDir(), doc.ActorID()+".automerge")
		if f, err := os.Create(tf); err != nil {
			slog.Error("failed to dump", "store", storeId, "err", err)
		} else {
			defer f.Close()
			if _, err := f.Write(doc.Save()); err != nil {
				slog.Error("failed to dump", "store", storeId, "err", err)
			}
		}
		slog.Info("dumped", "store", storeId, "path", tf)
		if svgPath, err := viz.RenderToTemp(doc, []interface{}{"counter"}); err != nil {
			slog.Error("failed to render", "store", storeId, "err", err)
		} else {
			slog.Info("rendered", "store", storeId, "path", "file://"+svgPath)
		}
		return true
	})

	return nil
}

type server struct {
	database *sql.DB
	cache    *sync.Map
}

func (s *server) init() error {
	if _, err := s.database.Exec(
		`CREATE TABLE IF NOT EXISTS stores (
    	id text not null primary key,
        content text                          
		)`,
	); err != nil {
		return err
	}
	if _, err := s.database.Exec(
		`INSERT OR IGNORE INTO stores (id, content) VALUES (?, ?)`,
		"default", base64.StdEncoding.EncodeToString(automerge.New().Save()),
	); err != nil {
		return err
	}
	s.cache = new(sync.Map)

	if res, err := s.database.Query(`SELECT id, content FROM stores`); err != nil {
		return fmt.Errorf("failed to query: %w", err)
	} else {
		defer func(res *sql.Rows) {
			if err := res.Close(); err != nil {
				slog.Error("failed to close: %w", err)
			}
		}(res)
		for res.Next() {
			var storeId string
			var rawSave string
			if err := res.Scan(&storeId, &rawSave); err != nil {
				return fmt.Errorf("failed to scan: %w", err)
			}
			if raw, err := base64.StdEncoding.DecodeString(rawSave); err != nil {
				return fmt.Errorf("failed to decode: %w", err)
			} else if doc, err := automerge.Load(raw); err != nil {
				return fmt.Errorf("failed to load doc: %w", err)
			} else {
				s.cache.Store(storeId, doc)
			}
		}
	}
	slog.Info("Ensured initial tables exist")
	return nil
}

func (s *server) getStore(writer http.ResponseWriter, request *http.Request) {
	vars := mux.Vars(request)
	fromCacheRaw, ok := s.cache.Load(vars["store"])
	if !ok {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	fromCache, ok := fromCacheRaw.(*automerge.Doc)
	if !ok {
		slog.Error("item in cache is not a doc")
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	fork, err := fromCache.Fork()
	if err != nil {
		slog.Error("failed to work", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.Header().Add("Content-Type", "application/octet-stream")
	if _, err := writer.Write(fork.Save()); err != nil {
		slog.Error("failed to write out", "err", err)
	}
}

func (s *server) syncStore(writer http.ResponseWriter, request *http.Request) {
	vars := mux.Vars(request)
	fromCacheRaw, ok := s.cache.Load(vars["store"])
	if !ok {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	fromCache, ok := fromCacheRaw.(*automerge.Doc)
	if !ok {
		slog.Error("item in cache is not a doc")
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		slog.Error("failed to upgrade", "err", err)
		return
	}
	defer conn.Close()

	syncState := automerge.NewSyncState(fromCache)
	if err := pkg.Sync(request.Context(), conn, syncState); err != nil {
		slog.Error("failed to sync", "err", err)
		_ = conn.Close()
	}
}

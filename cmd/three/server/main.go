package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/automerge/automerge-go"
	_ "github.com/mattn/go-sqlite3"
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
	db, err := sql.Open("sqlite3", "database.sqlite3")
	if err != nil {
		return err
	}
	defer db.Close()

	s := &server{database: db}

	if err := s.init(); err != nil {
		panic(err)
	}

	handlerWrapper := func(next http.HandlerFunc) http.HandlerFunc {
		return func(writer http.ResponseWriter, request *http.Request) {
			rw := &recordingWriter{inner: writer}
			next.ServeHTTP(rw, request)
			slog.Info("handled", "method", request.Method, "url", request.URL.String(), "status", rw.statusCode)
		}
	}
	http.DefaultServeMux.HandleFunc("/get", handlerWrapper(s.getCurrent))
	http.DefaultServeMux.HandleFunc("/new", handlerWrapper(s.createNew))
	http.DefaultServeMux.HandleFunc("/sync", handlerWrapper(s.sync))

	if err := http.ListenAndServe(*addrVar, http.DefaultServeMux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type recordingWriter struct {
	inner      http.ResponseWriter
	statusCode int
}

func (r *recordingWriter) Header() http.Header {
	return r.inner.Header()
}

func (r *recordingWriter) Write(bytes []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	return r.inner.Write(bytes)
}

func (r *recordingWriter) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.inner.WriteHeader(statusCode)
}

type server struct {
	database *sql.DB
}

func (s *server) init() error {
	slog.Info("Creating initial tables")
	if _, err := s.database.Exec(
		`CREATE TABLE IF NOT EXISTS stores (
    	id text not null primary key,
        snapshot_id text                          
		)`,
	); err != nil {
		return err
	}
	if _, err := s.database.Exec(
		`CREATE TABLE IF NOT EXISTS snapshots (
    	id text not null primary key,
    	store_id text not null,
    	content text not null
		)`,
	); err != nil {
		return err
	}
	return nil
}

func (s *server) getCurrent(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var rawContent string
	if err := s.database.QueryRowContext(
		request.Context(),
		`SELECT content FROM snapshots sn INNER JOIN stores st ON sn.id = st.snapshot_id WHERE st.id = $1`,
		"default",
	).Scan(&rawContent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		slog.Error("failed to query", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	rawData, err := base64.StdEncoding.DecodeString(rawContent)
	if err != nil {
		slog.Error("failed to base64 decode", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(writer).Encode(map[string]interface{}{
		"content": rawData,
	}); err != nil {
		slog.Error("failed to write", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func (s *server) createNew(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var inputs struct {
		Content []byte `json:"content"`
	}
	if err := json.NewDecoder(request.Body).Decode(&inputs); err != nil {
		slog.Error("failed to decode body", "err", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	slog.Info("attempting to load", "content", base64.StdEncoding.EncodeToString(inputs.Content))
	if _, err := automerge.Load(inputs.Content); err != nil {
		slog.Error("failed to load content", "err", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	tx, err := s.database.BeginTx(request.Context(), &sql.TxOptions{})
	if err != nil {
		slog.Error("failed to start tx", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	snapShotId := fmt.Sprintf("%d", time.Now().UnixNano())
	if _, err := tx.ExecContext(request.Context(), `INSERT INTO snapshots(id, store_id, content) VALUES (?, ?, ?)`, snapShotId, "default", base64.StdEncoding.EncodeToString(inputs.Content)); err != nil {
		slog.Error("failed to persist state", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	if _, err := tx.ExecContext(request.Context(), `INSERT INTO stores(id, snapshot_id) VALUES (?, ?)`, "default", snapShotId); err != nil {
		slog.Error("failed to persist state", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Error("failed to commit", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	writer.WriteHeader(http.StatusNoContent)
}

func (s *server) sync(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var inputs struct {
		Cookie   []byte   `json:"cookie"`
		Messages [][]byte `json:"messages"`
	}
	if err := json.NewDecoder(request.Body).Decode(&inputs); err != nil {
		slog.Error("failed to decode body", "err", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	slog.Info("opening tx")
	tx, err := s.database.BeginTx(request.Context(), &sql.TxOptions{})
	if err != nil {
		slog.Error("failed to start tx", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			slog.Error("failed to rollback", "err", err)
		}
	}()
	slog.Info("got tx")

	var doc *automerge.Doc
	var ss *automerge.SyncState
	outputMessages := make([][]byte, 0)

	if inputs.Cookie == nil {
		if len(inputs.Messages) > 0 {
			slog.Error("rejecting sync with messages without cookie", "err", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}

		var rawContent string
		if err := tx.QueryRowContext(
			request.Context(),
			`SELECT content FROM snapshots sn INNER JOIN stores st ON sn.id = st.snapshot_id WHERE st.id = ?`,
			"default",
		).Scan(&rawContent); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			slog.Error("failed to query", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		} else {
			decodedContent, err := base64.StdEncoding.DecodeString(rawContent)
			if err != nil {
				slog.Error("failed to decode", "err", err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			if doc, err = automerge.Load(decodedContent); err != nil {
				slog.Error("failed to load content", "err", err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		ss = automerge.NewSyncState(doc)

	} else {

		slog.Info("loading latest snapshot")
		var rawContent string
		if err := tx.QueryRowContext(
			request.Context(),
			`SELECT content FROM snapshots sn INNER JOIN stores st ON sn.id = st.snapshot_id WHERE st.id = ?`,
			"default",
		).Scan(&rawContent); err != nil {
			slog.Error("failed to query", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		slog.Info("decoding latest snapshot", "d#oc", len(rawContent))
		decodedContent, err := base64.StdEncoding.DecodeString(rawContent)
		if err != nil {
			slog.Error("failed to decode", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}

		doc, err = automerge.Load(decodedContent)
		if err != nil {
			slog.Error("failed to read the doc", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}

		ss, err = automerge.LoadSyncState(doc, inputs.Cookie)
		if err != nil {
			slog.Error("failed to load the cookie", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}

		slog.Info("generating messages")
		for i := 0; i < 100; i++ {
			msg, valid := ss.GenerateMessage()
			if !valid {
				break
			}
			outputMessages = append(outputMessages, msg.Bytes())
		}
		slog.Info("reciving messages")
		for _, message := range inputs.Messages {
			if _, err := ss.ReceiveMessage(message); err != nil {
				slog.Error("failed to apply a message", "err", err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
		}

		slog.Info("generating messages")
		for i := 0; i < 100; i++ {
			msg, valid := ss.GenerateMessage()
			if !valid {
				break
			}
			outputMessages = append(outputMessages, msg.Bytes())
		}
		slog.Info("generated messages", "m", len(outputMessages))

		slog.Info("doc heads", "heads", doc.Heads(), "map", doc.RootMap().GoString())

		snapShotId := fmt.Sprintf("%d", time.Now().UnixNano())

		finalState := base64.StdEncoding.EncodeToString(doc.Save())
		slog.Info("persisting state", "#doc", len(finalState), "snapshot", snapShotId)
		if res, err := tx.ExecContext(request.Context(), `INSERT INTO snapshots(id, store_id, content) VALUES (?, ?, ?)`, snapShotId, "default", finalState); err != nil {
			slog.Error("failed to persist state", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		} else if r, err := res.RowsAffected(); err != nil {
			slog.Error("failed to count rows affected by snapshot insert", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		} else if r == 0 {
			slog.Error("failed to update rows affected by snapshot insert!")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}

		if res, err := tx.ExecContext(request.Context(), `UPDATE stores SET snapshot_id = ? WHERE id = ?`, snapShotId, "default"); err != nil {
			slog.Error("failed to persist state", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		} else if r, err := res.RowsAffected(); err != nil {
			slog.Error("failed to count rows affected by store update", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		} else if r == 0 {
			slog.Error("no rows updated by store update!")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}

		slog.Info("committing")
		if err := tx.Commit(); err != nil {
			slog.Error("failed to commit", "err", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		slog.Info("committed tx")

	}

	finalCookie := ss.Save()
	if err := json.NewEncoder(writer).Encode(map[string]interface{}{
		"cookie":   finalCookie,
		"messages": outputMessages,
	}); err != nil {
		slog.Error("failed to encode response", "err", err)
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
}

package viz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/automerge/automerge-go"
	"github.com/goccy/go-graphviz"
	"github.com/goccy/go-graphviz/cgraph"
)

func RenderDocToSvg(doc *automerge.Doc, nodePath []interface{}, outputPath string) error {
	g := graphviz.New()

	graph, err := g.Graph()
	if err != nil {
		return fmt.Errorf("failed to setup graph: %w", err)
	}

	changes, err := doc.Changes()
	if err != nil {
		return fmt.Errorf("failed to generate changes: %w", err)
	}

	nodeMap := make(map[string]*cgraph.Node)
	var edgeCounter uint64
	for _, change := range changes {
		docAt, err := doc.Fork(change.Hash())
		if err != nil {
			return fmt.Errorf("failed to checkout %s: %w", change.Hash(), err)
		}
		var raw interface{}
		value, err := docAt.Path(nodePath...).Get()
		if err == nil {
			raw = value.Interface()
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return fmt.Errorf("failed to marshal %s: %w", change.Hash(), err)
		}

		n, err := graph.CreateNode(change.Hash().String())
		if err != nil {
			return fmt.Errorf("failed to create node: %w", err)
		}
		n.SetLabel(fmt.Sprintf("%s %s@%d %s", change.Hash().String()[:8], change.ActorID(), change.ActorSeq(), string(encoded)))
		nodeMap[n.Name()] = n

		for _, hash := range change.Dependencies() {
			_, err := graph.CreateEdge(strconv.Itoa(int(atomic.AddUint64(&edgeCounter, 1))), nodeMap[hash.String()], n)
			if err != nil {
				return fmt.Errorf("failed to create edge: %w", err)
			}
		}
	}

	var buff bytes.Buffer
	if err := g.Render(graph, graphviz.SVG, &buff); err != nil {
		return fmt.Errorf("failed to render: %w", err)
	}

	if err := os.WriteFile(outputPath, buff.Bytes(), os.ModePerm); err != nil {
		return fmt.Errorf("failed to write")
	}
	return nil
}

func RenderToTemp(doc *automerge.Doc, nodePath []interface{}) (string, error) {
	tf := filepath.Join(os.TempDir(), fmt.Sprintf("%d%d.svg", time.Now().UnixNano(), rand.Int()))
	if err := RenderDocToSvg(doc, nodePath, tf); err != nil {
		return "", err
	}
	return tf, nil
}

package pdf

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/choizeus02/paper-reader/backend/internal/domain"
)

func TestParseSText(t *testing.T) {
	in := `{"file":"x.pdf","pages":[
	  {"blocks":[
	    {"type":"text","bbox":{"x":10,"y":20,"w":100,"h":30},"lines":[
	      {"text":"Attention is all you need and posi-"},
	      {"text":"tion-wise layers"},
	      {"text":"   "}
	    ]},
	    {"type":"image","bbox":{"x":0,"y":0,"w":5,"h":5}},
	    {"type":"text","bbox":{"x":1,"y":2,"w":3,"h":4},"lines":[{"text":"  "}]},
	    {"type":"text","bbox":{"x":5,"y":60,"w":50,"h":8},"lines":[{"text":"Second"}]}
	  ]},
	  {"blocks":[]}
	]}`
	got, err := parseSText(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]domain.Block{
		{
			{ID: "b0", BBox: domain.BBox{X: 10, Y: 20, W: 100, H: 30}, Text: "Attention is all you need and posi-tion-wise layers"},
			{ID: "b1", BBox: domain.BBox{X: 5, Y: 60, W: 50, H: 8}, Text: "Second"},
		},
		{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	if got[1] == nil {
		t.Fatal("empty page must be an empty slice, not nil")
	}
}

func TestParseSTextBadJSON(t *testing.T) {
	if _, err := parseSText(strings.NewReader("{")); err == nil {
		t.Fatal("want error")
	}
}

func requireMutool(t *testing.T) string {
	bin, err := exec.LookPath("mutool")
	if err != nil {
		t.Skip("mutool not installed")
	}
	return bin
}

func TestProcessSamplePDF(t *testing.T) {
	bin := requireMutool(t)
	out := t.TempDir()
	pages, err := Extractor{Bin: bin, DPI: 72}.Process(context.Background(), "testdata/sample.pdf", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("want 2 pages, got %d", len(pages))
	}
	p1 := pages[0]
	if p1.PageNo != 1 || p1.Width != 300 || p1.Height != 200 || p1.ImagePath != "001.png" {
		t.Fatalf("page 1 meta wrong: %+v", p1)
	}
	if len(p1.Blocks) != 2 || p1.Blocks[0].Text != "Hello paper reader." || p1.Blocks[1].Text != "Second block here." {
		t.Fatalf("page 1 blocks wrong: %+v", p1.Blocks)
	}
	if p1.Blocks[0].BBox.Y >= p1.Blocks[1].BBox.Y {
		t.Fatalf("bbox must use top-left origin: %+v", p1.Blocks)
	}
	if pages[1].Blocks == nil || len(pages[1].Blocks) != 0 || pages[1].ImagePath != "002.png" {
		t.Fatalf("page 2 should have empty blocks: %+v", pages[1])
	}
	for _, name := range []string{"001.png", "002.png"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "stext.json")); !os.IsNotExist(err) {
		t.Fatal("stext.json should be removed after processing")
	}
}

func TestProcessCorruptPDF(t *testing.T) {
	bin := requireMutool(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pdf")
	if err := os.WriteFile(bad, []byte("%PDF-1.4 broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Extractor{Bin: bin, DPI: 72}.Process(context.Background(), bad, filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "mutool") {
		t.Fatalf("want mutool error, got %v", err)
	}
}

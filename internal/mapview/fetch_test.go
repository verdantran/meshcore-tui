package mapview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// listing serves a TileJSON whose tile URL is tiles(its own URL), and hands every other path to rest.
func listing(t *testing.T, tiles func(self string) string, rest http.HandlerFunc) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/planet" {
			w.Write([]byte(`{"tiles":["` + tiles(srv.URL) + `"]}`))
			return
		}
		rest(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchOne(srv *httptest.Server, dir string) error {
	_, _, err := Fetcher{Index: srv.URL + "/planet", Dir: dir, Client: srv.Client()}.Fetch(context.Background(), nil, []Tile{{1, 1}}, nil)
	return err
}

func TestFetchStaysOnTheIndexHost(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		w.Write(sampleTile())
	}))
	defer other.Close()
	tile := func(w http.ResponseWriter, r *http.Request) { w.Write(sampleTile()) }

	for name, tiles := range map[string]func(string) string{
		"plain http":   func(self string) string { return strings.Replace(self, "https:", "http:", 1) + "/t/{z}/{x}/{y}.pbf" },
		"another host": func(string) string { return other.URL + "/t/{z}/{x}/{y}.pbf" },
		"a file":       func(string) string { return "file:///etc/passwd" },
	} {
		if err := fetchOne(listing(t, tiles, tile), ""); err == nil || !strings.Contains(err.Error(), "anywhere but https") {
			t.Errorf("%s: %v", name, err)
		}
	}

	same := func(self string) string { return self + "/t/{z}/{x}/{y}.pbf" }
	var self string
	for name, to := range map[string]func() string{
		"plain http":   func() string { return strings.Replace(self, "https:", "http:", 1) + "/u" },
		"another host": func() string { return other.URL + "/u" },
	} {
		srv := listing(t, same, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to(), http.StatusFound) })
		self = srv.URL
		if err := fetchOne(srv, ""); err == nil || !strings.Contains(err.Error(), "anywhere but https") {
			t.Errorf("redirect to %s: %v", name, err)
		}
	}
	if elsewhere.Load() != 0 {
		t.Errorf("%d requests reached another host", elsewhere.Load())
	}

	srv := listing(t, same, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/u" {
			http.Redirect(w, r, "/u", http.StatusFound)
			return
		}
		w.Write(sampleTile())
	})
	if err := fetchOne(srv, ""); err != nil {
		t.Errorf("a redirect on the same host: %v", err)
	}
	if _, _, err := (Fetcher{Index: "http://tiles.example/planet"}).Fetch(context.Background(), nil, []Tile{{1, 1}}, nil); err == nil || !strings.Contains(err.Error(), "anywhere but https") {
		t.Errorf("an index over plain http: %v", err)
	}
}

func TestFetchRefusesTooMuch(t *testing.T) {
	srv := listing(t, func(self string) string { return self + "/t/{z}/{x}/{y}.pbf" }, func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, maxResponse+1))
	})
	if err := fetchOne(srv, ""); err == nil || !strings.Contains(err.Error(), "more than 8 MB") {
		t.Errorf("an oversized tile: %v", err)
	}
}

func TestTileCacheIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits or plain symlinks")
	}
	srv := listing(t, func(self string) string { return self + "/t/{z}/{x}/{y}.pbf" }, func(w http.ResponseWriter, r *http.Request) {
		w.Write(sampleTile())
	})
	dir := filepath.Join(t.TempDir(), "tiles")
	os.Mkdir(dir, 0o700)
	cache := filepath.Join(dir, "10-1-1.pbf")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(outside, cache); err != nil {
		t.Fatal(err)
	}
	if err := fetchOne(srv, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("the tile went through the symlink")
	}
	if fi, err := os.Lstat(cache); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("cached tile: %v %v", fi.Mode(), err)
	}

	open := filepath.Join(t.TempDir(), "tiles")
	os.Mkdir(open, 0o700)
	os.Chmod(open, 0o777)
	if err := fetchOne(srv, open); err == nil || !strings.Contains(err.Error(), "others can write") {
		t.Errorf("a folder anyone can write to: %v", err)
	}
	if err := (&Detail{}).Save(filepath.Join(open, "detail.json.gz")); err == nil {
		t.Error("detail saved into a folder anyone can write to")
	}
}

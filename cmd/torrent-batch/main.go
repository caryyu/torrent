package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"

	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/tagflag"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/util/dirwatch"
	"github.com/dustin/go-humanize"
)

var (
	args = struct {
		MetainfoDir string `help:"torrent files in this location describe the contents of the mounted filesystem"`
		DownloadDir string `help:"location to save torrent data"`

		DisableTrackers bool
		ReadaheadBytes  tagflag.Bytes
		ListenAddr      *net.TCPAddr
	}{
		MetainfoDir: func() string {
			_user, err := user.Current()
			if err != nil {
				log.Fatal(err)
			}
			return filepath.Join(_user.HomeDir, ".config/transmission/torrents")
		}(),
		ReadaheadBytes: 10 << 20,
		ListenAddr:     &net.TCPAddr{},
	}
)

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	tagflag.Parse(&args)
	defer envpprof.Stop()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = args.DownloadDir
	cfg.DisableTrackers = args.DisableTrackers
	cfg.SetListenAddr(args.ListenAddr.String())
	client, err := torrent.NewClient(cfg)
	if err != nil {
		log.Print(err)
		return 1
	}
	// This is naturally exported via GOPPROF=http.
	http.DefaultServeMux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	http.DefaultServeMux.HandleFunc("/pretty", func(w http.ResponseWriter, req *http.Request) {
		torrents := client.Torrents()
		sort.Slice(torrents, func(i, j int) bool {
			t1 := torrents[i].InfoHash().HexString()
			t2 := torrents[j].InfoHash().HexString()
			return t1 < t2
		})
		for _, t := range torrents {
			var completedPieces, partialPieces int
			psrs := t.PieceStateRuns()
			for _, r := range psrs {
				if r.Complete {
					completedPieces += r.Length
				}
				if r.Partial {
					partialPieces += r.Length
				}
			}
			fmt.Fprintf(w,
				"downloading %q: %s/%s, %d/%d pieces completed (%d partial)\n",
				t.Name(),
				humanize.Bytes(uint64(t.BytesCompleted())),
				humanize.Bytes(uint64(t.Length())),
				completedPieces,
				t.NumPieces(),
				partialPieces,
			)
		}
	})
	dw, err := dirwatch.New(args.MetainfoDir)
	if err != nil {
		log.Printf("error watching torrent dir: %s", err)
		return 1
	}

	for ev := range dw.Events {
		var t *torrent.Torrent
		var err error
		switch ev.Change {
		case dirwatch.Added:
			if ev.TorrentFilePath != "" {
				t, err = client.AddTorrentFromFile(ev.TorrentFilePath)
				if err != nil {
					log.Printf("error adding torrent to client: %s", err)
				}
			} else if ev.MagnetURI != "" {
				t, err = client.AddMagnet(ev.MagnetURI)
				if err != nil {
					log.Printf("error adding magnet: %s", err)
				}
			}
		case dirwatch.Removed:
			T, ok := client.Torrent(ev.InfoHash)
			if !ok {
				break
			}
			T.Drop()
		}

		if t != nil && !t.Completed() {
			go func() {
				<-t.GotInfo()
				t.DownloadAll()
			}()
		}
	}
	return 0
}

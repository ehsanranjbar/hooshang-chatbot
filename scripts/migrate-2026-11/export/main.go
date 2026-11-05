// Command export is the first half of the one-off data move planned for the
// memory refactor (PLAN.md, "Data move"): it reads a hooshang data directory
// with the current store package and writes everything worth carrying over to
// one JSON file. The import half comes with the refactor.
//
//	go run ./scripts/migrate-2026-11/export -data ./data -out dump.json
//
// Stop the bot first (Badger allows one process per directory). The program
// only reads records; opening the database with the current code may still
// register new keyspaces in it, as the bot itself does at startup.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ehsanranjbar/hooshang/internal/store"
)

// Dump is the whole export. Field names are the Go names of the store types,
// so the import half can decode a section straight into them.
type Dump struct {
	ExportedAt time.Time
	Chats      []ChatDump
	Members    []MemberDump
	Events     []EventDump
	Terms      []TermDump
	Media      []store.MediaDesc // the shared sticker/GIF cache
}

// ChatDump is one chat's settings.
type ChatDump struct {
	ID         int64
	Title      string
	Muted      bool
	ModActions map[string]bool // nil = the config default
}

// MemberDump is one member of one chat with the full memory profile.
type MemberDump struct {
	Chat             int64
	User             int64
	FirstName        string
	LastName         string
	Username         string
	NoTease          bool
	Ignored          bool
	Profile          store.Profile // every section with its Locked flags, Orders and Suppressed
	ProfileUpdatedAt time.Time
}

// EventDump is one remembered group event.
type EventDump struct {
	Chat    int64
	Text    string
	Score   int
	FirstAt time.Time
	LastAt  time.Time
}

// TermDump is one glossary term.
type TermDump struct {
	Chat    int64
	Term    string
	Norm    string
	Meaning string
	ByID    int64
	ByName  string
	AddedAt time.Time
	UsedAt  time.Time
}

// forever is a TTL long enough to keep every group event, however old.
const forever = 100 * 365 * 24 * time.Hour

func main() {
	data := flag.String("data", "./data", "hooshang data directory (the bot must be stopped)")
	out := flag.String("out", "dump.json", "file to write")
	flag.Parse()
	if err := run(*data, *out); err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		os.Exit(1)
	}
}

// run opens the data directory, builds the dump and writes it to out.
func run(dataDir, out string) error {
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		return fmt.Errorf("data directory %q does not exist", dataDir) // Open would create an empty one
	}
	db, err := store.Open(dataDir, time.Hour)
	if err != nil {
		return fmt.Errorf("open %s: %w", dataDir, err)
	}
	d, err := build(db)
	if err != nil {
		return errors.Join(err, db.Close())
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := write(out, d); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d chats, %d members, %d events, %d terms, %d media\n",
		out, len(d.Chats), len(d.Members), len(d.Events), len(d.Terms), len(d.Media))
	return nil
}

// build reads everything into a Dump.
func build(db *store.DB) (*Dump, error) {
	d := &Dump{ExportedAt: time.Now().UTC()}
	ids, err := db.ChatIDs()
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	for _, id := range ids {
		if err := d.addChat(db, id); err != nil {
			return nil, fmt.Errorf("chat %d: %w", id, err)
		}
	}
	if d.Media, err = db.AllMedia(); err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	return d, nil
}

// addChat adds a chat with its members, events and terms.
func (d *Dump) addChat(db *store.DB, id int64) error {
	c, err := db.GetChat(id)
	if err != nil {
		return err
	}
	d.Chats = append(d.Chats, ChatDump{ID: id, Title: c.Title, Muted: c.Muted, ModActions: c.ModActions})
	members, err := db.Members(id)
	if err != nil {
		return err
	}
	for _, m := range members {
		d.Members = append(d.Members, MemberDump{
			Chat: id, User: m.UserID(), FirstName: m.FirstName, LastName: m.LastName, Username: m.Username,
			NoTease: m.NoTease, Ignored: m.Ignored, Profile: m.Profile, ProfileUpdatedAt: m.ProfileUpdatedAt,
		})
	}
	events, err := db.GroupEvents(id, forever)
	if err != nil {
		return err
	}
	for _, e := range events {
		d.Events = append(d.Events, EventDump{Chat: id, Text: e.Text, Score: e.Score, FirstAt: e.FirstAt, LastAt: e.LastAt})
	}
	terms, err := db.Terms(id)
	if err != nil {
		return err
	}
	for _, t := range terms {
		d.Terms = append(d.Terms, TermDump{Chat: id, Term: t.Term, Norm: t.Norm, Meaning: t.Meaning,
			ByID: t.ByID, ByName: t.ByName, AddedAt: t.AddedAt, UsedAt: t.UsedAt})
	}
	return nil
}

// write saves the dump as indented JSON.
func write(path string, d *Dump) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

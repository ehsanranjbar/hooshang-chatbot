// Command promptdump prints the prompt hooshang would send to the model for a
// reply to a stored message, built from the database.
//
// Badger allows one process at a time, so while the bot runs, point -data at a
// copy (on macOS: cp -Rc data /tmp/hooshang-data).
//
//	go run ./cmd/promptdump -data /tmp/hooshang-data            # list chats
//	go run ./cmd/promptdump -data /tmp/hooshang-data -chat -100… [-msg 123] [-full]
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ehsanranjbar/hooshang/internal/auth"
	"github.com/ehsanranjbar/hooshang/internal/bot"
	"github.com/ehsanranjbar/hooshang/internal/config"
	"github.com/ehsanranjbar/hooshang/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "the YAML config")
	data := flag.String("data", "", "the Badger directory; default storage.path (use a copy while the bot runs)")
	chat := flag.Int64("chat", 0, "chat id; 0 lists the chats")
	msg := flag.Int64("msg", 0, "message id to reply to; 0 = the latest member message")
	full := flag.Bool("full", false, "include the chance-gated context (author profile, group memory)")
	botID := flag.Int64("bot", 0, "the bot's user id (so its own profile is skipped)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	if *data == "" {
		*data = cfg.Storage.Path
	}
	db, err := store.Open(*data, cfg.Bot.MessageRetention)
	if err != nil {
		fail(err)
	}
	defer db.Close()

	if *chat == 0 {
		ids, err := db.ChatIDs()
		if err != nil {
			fail(err)
		}
		for _, id := range ids {
			c, _ := db.GetChat(id)
			msgs, _ := db.RecentMessages(id, 1000)
			fmt.Printf("%d\t%s\t%d messages\n", id, c.Title, len(msgs))
		}
		return
	}

	req, err := bot.DumpPrompt(bot.Deps{Cfg: cfg, DB: db, Admins: &auth.Owner{ID: cfg.Telegram.OwnerID}}, *botID, *chat, *msg, *full)
	if err != nil {
		fail(err)
	}
	for _, m := range req.Messages {
		fmt.Printf("===== %s =====\n%s\n\n", strings.ToUpper(m.Role), m.Content)
	}
	if len(req.Tools) > 0 {
		var names []string
		for _, t := range req.Tools {
			names = append(names, t.Name)
		}
		fmt.Printf("===== TOOLS =====\n%s\n", strings.Join(names, ", "))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "promptdump:", err)
	os.Exit(1)
}

// Command sourcecheck runs a source plugin through the whole conversation the
// app would have with it and says what does not hold.
//
// It exists so that somebody writing a plugin can find out before installing
// anything, and it is the same code the app's own tests run — a checker that
// lived only on our side would drift from what the app accepts, and an author
// would learn the difference from a user.
//
//	go run ./cmd/sourcecheck -- npx -y @example/xxvi-source-gmail
//	go run ./cmd/sourcecheck -config label=INBOX -- ./my-plugin
//
// It never touches a board: a plugin has nothing to do with one.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/artipop/xxvi/internal/sources/plugin"
)

func main() {
	var (
		config  = flag.String("config", "", "поля плагина: ключ=значение, через запятую")
		token   = flag.String("token", "", "токен, который приложение передало бы плагину")
		dir     = flag.String("dir", "", "рабочий каталог плагина")
		timeout = flag.Duration("timeout", 30*time.Second, "сколько ждать плагин целиком")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Использование: %s [флаги] -- команда запуска плагина\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	command := flag.Args()
	if len(command) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	findings := plugin.Check(context.Background(), plugin.CheckOptions{
		Command: command,
		Dir:     *dir,
		Config:  parseConfig(*config),
		Token:   *token,
		Timeout: *timeout,
	})
	if len(findings) == 0 {
		fmt.Println("✓ плагин отвечает так, как приложение ожидает")
		return
	}

	fatal := false
	for _, f := range findings {
		fmt.Println(f)
		fatal = fatal || f.Fatal
	}
	// A non-fatal finding still means something will go wrong later, so it is
	// not silent — but only a fatal one is a failed run, because an author
	// fixing warnings one at a time should see the rest pass.
	if fatal {
		os.Exit(1)
	}
}

func parseConfig(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

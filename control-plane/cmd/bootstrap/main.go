// Command bootstrap creates the first account and API key.
//
// Minting a key through the API requires already holding one, so the very
// first credential has to come from somewhere with direct database access.
// This is that somewhere: an operator task, not a public signup endpoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/config"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
	"github.com/KaumudiRRawal/launchpad/control-plane/internal/store"
	"github.com/KaumudiRRawal/launchpad/control-plane/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	email := flag.String("email", "", "email address for the account (required)")
	name := flag.String("name", "", "display name for the account (required)")
	keyName := flag.String("key-name", "bootstrap", "label for the generated API key")
	flag.Parse()

	if *email == "" || *name == "" {
		flag.Usage()
		return errors.New("both -email and -name are required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Bootstrap output is a credential, so the logger is silenced to keep
	// anything else off stdout.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := store.Connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool, migrations.FS, log); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	repo := store.NewRepository(pool)

	account, err := repo.CreateAccount(ctx, *email, *name)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return fmt.Errorf("an account already exists for %s", *email)
		}
		return fmt.Errorf("create account: %w", err)
	}

	key, token, err := repo.CreateAPIKey(ctx, account.ID, *keyName)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}

	fmt.Printf("account id:  %s\n", account.ID)
	fmt.Printf("api key id:  %s\n", key.ID)
	fmt.Printf("token:       %s\n", token)
	fmt.Println()
	fmt.Println("This token is shown once and cannot be recovered. Store it now.")
	return nil
}

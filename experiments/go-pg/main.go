// goclient — a tiny *Go* binary that connects to Postgres and runs
// `select version()`. It's the test subject for plug's Go coverage:
//
//   - Go bypasses libc on Linux (raw syscalls) → the current LD_PRELOAD hook
//     can't see its connect(). This binary is exactly the "uncovered" case.
//   - On macOS, Go goes through libSystem → running it under plug also tells us
//     whether the current DYLD hook already catches Go.
//
// It reads its target from the env (12-factor), loading a local .env if present.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func main() {
	_ = godotenv.Load() // load ./.env if present (ignored if absent)

	host := env("PG_HOST", "")
	port := env("PG_PORT", "5432")
	user := env("PG_USER", "postgres")
	pwd := env("PG_PWD", "")
	dbname := env("PG_DB", "postgres")

	if host == "" {
		fmt.Fprintln(os.Stderr, "[goclient] set PG_HOST in .env — use a CLUSTER service name (e.g. odb),")
		fmt.Fprintln(os.Stderr, "           reachable ONLY through the cluster, so success proves interception.")
		os.Exit(2)
	}

	fmt.Printf("[goclient] Go binary → dialing postgres %s:%s (db=%s, user=%s)\n", host, port, dbname, user)

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=8",
		host, port, user, pwd, dbname)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Printf("[goclient] open FAILED: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetConnMaxLifetime(10 * time.Second)

	var version string
	if err := db.QueryRow("select version()").Scan(&version); err != nil {
		fmt.Printf("[goclient] connect/query FAILED: %v\n", err)
		fmt.Println("[goclient] → if PG_HOST is cluster-only, the connect() was NOT intercepted (Go not covered here)")
		os.Exit(1)
	}
	fmt.Printf("[goclient] ✅ reached postgres through the network:\n           %s\n", version)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

# API Encryptor
---

My own encryption implementation for my website to share files

> **Status:** early development. Files can be encrypted into storage and
> decrypted back out with temporary `debug` commands, and `filedrop serve`
> runs the cleaner that removes expired, used-up and revoked files. The HTTP
> server (uploading and downloading over the web) is not built yet.

## Requirements

- Go 1.27 or newer
- `sqlite3` (optional, only for inspecting the database by hand)

## Build

```sh
go build -o filedrop ./cmd/filedrop
```

This produces a single `filedrop` binary. Every command below is a subcommand of it.

## Configuration

Settings come from environment variables, normally kept in a `.env` file.
Copy the example and edit it:

```sh
cp .env.example .env
```

| Variable | What it sets | Default | Flag override |
| --- | --- | --- | --- |
| `FILEDROP_DB` | SQLite database file | `~/.local/share/filedrop/filedrop.db` | `--db PATH` |
| `FILEDROP_MASTER_KEY` | Master key file | `~/.local/share/filedrop/master.key` | `--master-key PATH` |
| `FILEDROP_DATA_DIR` | Directory for encrypted files | `~/.local/share/filedrop/files` | none |
| `FILEDROP_MAX_FILE_BYTES` | Largest single upload, in bytes | `2147483648` (2 GiB) | none |
| `FILEDROP_MAX_TOTAL_BYTES` | Total size of all stored files, in bytes | `10737418240` (10 GiB) | none |
| `FILEDROP_SWEEP_INTERVAL` | How often the cleaner runs (Go duration, at least `1s`) | `5m` | none |

A flag beats the environment variable, which beats the default. Directories are
created automatically. The database schema is created or upgraded automatically
whenever a command opens it.

`.env` is git-ignored; never commit it.

### Loading `.env`

Go doesn't read `.env` by itself; something has to turn it into environment
variables.

- **On the server (systemd):** the service unit loads it with
  `EnvironmentFile=/path/to/.env`.
- **For CLI commands in a shell** (on the server or this desktop), load it first
  so the CLI uses the same database and key as the service:

  ```sh
  set -a; . ./.env; set +a
  ./filedrop keys list
  ```

  Without this step, the CLI falls back to the default paths, which on the server
  would be the wrong database.

On the server, keep the master key on a **different drive** from the database and
uploaded files, owned by the service's own user.

## First-time setup

### 1. Create the master key

```sh
./filedrop masterkey init
```

This writes 32 random bytes to the master key file with permissions `0400`
(readable only by its owner). It will **never overwrite** an existing key: if
the file is already there, the command fails. Losing or replacing the master key
makes every stored file unreadable.

Back the key up somewhere separate from the database and the uploaded files.

### 2. Create the database

There is no separate step: the database file and its tables (`api_keys` and
`files`) are created automatically the first time any command opens it. Running
the next step, or simply

```sh
./filedrop keys list
```

creates them at the path in `FILEDROP_DB`.

How it works: the table definitions live in `internal/db/migrations/` as numbered
SQL files (`0001_init.sql`, ...) that are built into the binary. SQLite stores
the number of the last one applied (`PRAGMA user_version`). Each time a command
opens the database, any newer migration files are applied once, in order, so
upgrading the binary also upgrades the schema.

To look at the tables yourself:

```sh
sqlite3 "$FILEDROP_DB" '.tables'            # api_keys  files
sqlite3 "$FILEDROP_DB" '.schema files'      # full table definition
sqlite3 "$FILEDROP_DB" 'PRAGMA user_version' # last migration applied
```

### 3. Create an API key

```sh
./filedrop keys create --name laptop
```

The key (it starts with `fd_`) is printed **once**. Only its SHA-256 hash is
saved, so it can't be shown again. If you lose it, create a new one.

The key is the only thing written to standard output, so you can capture it:

```sh
KEY=$(./filedrop keys create --name laptop)
```

Each key needs a unique name of at most 64 characters.

## Commands

| Command | What it does |
| --- | --- |
| `filedrop masterkey init` | Create the master key (refuses to overwrite) |
| `filedrop keys create --name NAME` | Create an API key and print it once |
| `filedrop keys list` | List keys: ID, name, creation time, revoked status (never the keys themselves) |
| `filedrop keys revoke NAME` | Revoke a key; its files become undownloadable at once and are removed from disk by the next sweep |
| `filedrop serve` | Run the service. For now this is only the cleaner; stop it with Ctrl-C or SIGTERM |
| `filedrop debug encrypt --key NAME [--downloads N] [--expires DUR] PATH` | *Temporary.* Encrypt a local file into storage and print its ID |
| `filedrop debug decrypt -o OUT ID` | *Temporary.* Decrypt a stored file to `OUT` (`-` for standard output) |

## Running the service and cleanup

```sh
set -a; . ./.env; set +a
./filedrop serve
```

`serve` runs until stopped with Ctrl-C (or SIGTERM, which systemd sends). Logs
go to standard error; under systemd, `journalctl` shows them. Logs show only
the first 6 characters of a file ID, because the full ID is the secret part of
a share link.

For now `serve` only runs the **cleaner**, the one part of the program that
deletes files. On startup it releases any download slots left over from a
previous run and abandons unfinished uploads. It then sweeps immediately and
again every `FILEDROP_SWEEP_INTERVAL`. Each sweep:

1. marks files that have **expired** or **used up their downloads**, and that
   nobody is currently downloading, as deleted, which also frees their storage;
2. removes the encrypted data of every deleted file from disk.

A file being downloaded is never removed mid-download; the next sweep after
the download finishes takes care of it.

### Revoking a key

```sh
./filedrop keys revoke laptop
```

The key stops working, and every file it uploaded becomes undownloadable
immediately. Those files are removed from disk on the cleaner's next sweep, so
`serve` must be running (or be started later) for that to happen. Revoking
can't be undone; create a new key instead.

## Trying encryption (temporary `debug` commands)

Until the HTTP server exists, these two commands run the same code that uploads
and downloads will use. They will be removed once uploads and downloads work
over HTTP.

```sh
set -a; . ./.env; set +a                   # same settings as the service

# Encrypt: prints the file's ID (the secret part of a future share link)
ID=$(./filedrop debug encrypt --key laptop --downloads 2 --expires 2h photo.jpg)

# Decrypt to a new file (refuses to overwrite an existing one)
./filedrop debug decrypt -o copy.jpg "$ID"

# Or to standard output, e.g. to compare with the original
./filedrop debug decrypt -o - "$ID" | sha256sum
sha256sum photo.jpg
```

What happens:

- **Encrypt** checks the size limits and reserves space, creates a new random
  key for this file and wraps it with the master key, then streams the file
  through encryption into `FILEDROP_DATA_DIR/<ID>`. The unencrypted contents are
  never written to disk. If anything fails, the partial file is removed and the
  space is given back.
- **Decrypt counts as a download.** Once the download limit is used up, further
  decrypts fail with "no downloads left". A failed decrypt doesn't use one up.
- Defaults: `--downloads 1` (use `-1` for unlimited), `--expires 24h` (the maximum).
- Expired and used-up files are removed by the cleaner in `filedrop serve`.
- Don't start `serve` while a `debug` command is running in another terminal:
  `serve`'s startup treats in-progress uploads and downloads as leftovers from
  a crash. (Once HTTP exists, all of these run inside `serve`.)

## Development

```sh
go vet ./...
go test ./...
```

To try the commands without touching your real files, point them at a
throwaway folder:

```sh
export FILEDROP_DB=/tmp/filedrop-test/filedrop.db
export FILEDROP_MASTER_KEY=/tmp/filedrop-test/master.key
```

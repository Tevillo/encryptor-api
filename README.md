# API Encryptor
---

My own encryption implementation for my website to share files

> **Status:** early development. The database, API keys and master key work.
> The HTTP server (uploading and downloading files) is not built yet.

## Requirements

- Go 1.27 or newer
- `sqlite3` (optional, only for inspecting the database by hand)

## Build

```sh
go build -o filedrop ./cmd/filedrop
```

This produces a single `filedrop` binary. Every command below is a subcommand of it.

## Where files live

| What | Flag | Environment variable | Default |
| --- | --- | --- | --- |
| Database (SQLite) | `--db PATH` | `FILEDROP_DB` | `~/.local/share/filedrop/filedrop.db` |
| Master key | `--master-key PATH` | `FILEDROP_MASTER_KEY` | `~/.local/share/filedrop/master.key` |

A flag beats the environment variable, which beats the default. Directories are
created automatically. The database schema is created or upgraded automatically
whenever a command opens it.

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

### 2. Create an API key

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

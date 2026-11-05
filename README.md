# API Encryptor
---

My own encryption implementation for my website to share files

> **Status:** early development. `filedrop serve` accepts encrypted uploads
> and serves downloads over HTTP, and its cleaner removes expired, used-up and
> revoked files. Not done yet: deployment behind Caddy/systemd and a browser
> frontend.

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
| `FILEDROP_LISTEN` | Address the HTTP server listens on | `127.0.0.1:8090` | none |
| `FILEDROP_PUBLIC_URL` | Start of every share link (no trailing slash) | `http://<FILEDROP_LISTEN>/filedrop` | none |

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
| `filedrop serve` | Run the service: the HTTP server and the cleaner. Stop it with Ctrl-C or SIGTERM |

## Running the service and cleanup

```sh
set -a; . ./.env; set +a
./filedrop serve
```

`serve` runs the HTTP server on `FILEDROP_LISTEN` and the cleaner, until
stopped with Ctrl-C (or SIGTERM, which systemd sends). On stop it accepts no
new requests and gives transfers in progress up to 30 seconds to finish.

Logs go to standard error; under systemd, `journalctl` shows them. Requests are
logged by route (`GET /filedrop/{id}`), never by their real path, and file IDs
are cut to 6 characters, because the full ID is the secret part of a share link.

### Cleanup

The **cleaner** is the one part of the program that deletes files. On startup
it releases any download slots left over from a previous run and abandons
unfinished uploads, before the HTTP server accepts requests. It then sweeps
immediately and again every `FILEDROP_SWEEP_INTERVAL`. Each sweep:

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
immediately. Downloads already in progress are cut off within about 10 seconds.
The files are removed from disk on the cleaner's next sweep. Revoking can't be
undone; create a new key instead.

## Uploading and downloading

Examples use `https://paulborrego.com/filedrop`; locally, use your
`FILEDROP_PUBLIC_URL` (by default `http://127.0.0.1:8090/filedrop`).

### Upload

```sh
curl -T "holiday photo.jpg" \
     -H "Authorization: Bearer $KEY" \
     --url-query expires=2h --url-query downloads=3 \
     https://paulborrego.com/filedrop/
```

The response is the share link, on one line:

```
https://paulborrego.com/filedrop/lLwqZ6_kBjVaTSUo0ww8mA
```

so `LINK=$(curl ...)` captures it.

- `-T FILE` sends the file as the request body. Because the URL ends in `/`,
  curl adds the file name to it (`.../filedrop/holiday%20photo.jpg`), and that
  name is what the recipient's download is saved as.
- **Use `--url-query` for settings, not `?` in the URL.** curl only adds the
  file name when the URL ends in `/` with nothing after it; with
  `.../filedrop/?expires=2h` the name is dropped and the server answers `404`.
  (Alternatively, write the name yourself:
  `.../filedrop/holiday%20photo.jpg?expires=2h`.)
- The file is encrypted as it arrives; the unencrypted contents never touch the
  server's disk.

| Setting | Values | Default |
| --- | --- | --- |
| `expires` | Go duration from `1s` to `24h`, e.g. `30m`, `2h` | `24h` |
| `downloads` | `-1` (unlimited) or at least `1` | `1` |

Any other setting name is rejected, so a typo like `expire=2h` fails instead of
silently using the default.

### Download

Anyone with the link can download; no key is needed:

```sh
curl -OJ https://paulborrego.com/filedrop/lLwqZ6_kBjVaTSUo0ww8mA
```

`-O -J` saves the file under its original name. Each successful download uses
one of the file's downloads; a download that fails partway doesn't. While all
remaining downloads are in progress at once, further requests get `409`.
`HEAD` requests are refused so they can't use up a download.

**Link previews:** chat apps (Slack, iMessage, ...) often fetch pasted links to
build a preview, and that fetch counts as a download. Share links where that
won't happen, or allow more than one download.

### Responses

| Status | Meaning |
| --- | --- |
| `201` | Uploaded; the body is the share link |
| `200` | Download follows |
| `400` | Invalid setting, file name, or the upload didn't complete |
| `401` | Missing, unknown or revoked API key |
| `404` | No such file (or no file name in the upload URL) |
| `405` | Wrong method, e.g. `HEAD` on a link |
| `409` | All of the file's remaining downloads are in progress; try again later |
| `410` | Expired, used up, or deleted |
| `411` | No `Content-Length`; upload a file, not a pipe |
| `413` | Larger than `FILEDROP_MAX_FILE_BYTES` |
| `507` | Total storage (`FILEDROP_MAX_TOTAL_BYTES`) is full |

Errors are a single line of text starting with `error:`.

A transfer that makes no progress for 60 seconds is cut off, and a cut-off
upload leaves nothing behind.

### Not built yet

- Deployment: a systemd unit and the Caddy route for `/filedrop/*`.
- A browser frontend. It will need its own way to authenticate senders, since
  an API key embedded in a web page is visible to anyone using it.

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
export FILEDROP_DATA_DIR=/tmp/filedrop-test/files
```

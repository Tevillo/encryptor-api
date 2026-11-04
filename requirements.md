# Encrypted File-Drop Service — Requirements

Source: *Backend Roadmap: Go + AWS Solutions Architect Associate* (Oct 20, 2026). Written 2026-11-03.

## Decisions

| Topic | Decision |
| --- | --- |
| Encryption | Server-side, as in the roadmap: S3 objects encrypted with a customer-managed KMS key. Not end-to-end. |
| Client | API only. Use it with `curl`; the README documents every call. No web UI or CLI. |
| Auth | Senders need an API key. Recipients only need the share link. |
| Link controls | Expiry time and a size cap. No download limit or link password in the MVP. |
| Scope | Only the project. The Go and SAA study plan stays in the roadmap. |

Defaults below marked *(proposed)* are suggestions you can change.

## 1. Use case (definition of done)

- [ ] A sender with an API key asks for an upload and gets back a short-lived presigned upload URL plus a file ID.
- [ ] The sender uploads straight to S3; the object is encrypted at rest with KMS.
- [ ] The API stores the file's metadata in Postgres and returns a share link.
- [ ] The recipient opens the share link and is redirected to a short-lived presigned download URL.
- [ ] The worker picks jobs off SQS to hash the file, enforce expiry, and delete it from S3 once expired.
- [ ] All of this runs at `api.paulborrego.com` over HTTPS, deployed by Terraform through GitHub Actions.

## 2. Go REST API

### Endpoints
- [ ] `POST /v1/uploads`: needs an API key. Takes the filename, size, content type and expiry. Returns the file ID, a presigned upload URL (or form fields) and the share URL.
- [ ] `POST /v1/uploads/{id}/complete` *(or an S3 event, see §5)*: marks the upload finished and queues the hash job.
- [ ] `GET /f/{id}`: public. Returns `404`/`410` for files that are missing, expired or not finished; otherwise sends a `302` to a presigned GET URL.
- [ ] `GET /v1/files/{id}`: needs an API key and the caller must own the file. Returns status, size, SHA-256 and expiry.
- [ ] `DELETE /v1/files/{id}`: needs an API key and ownership. Deletes the file early.
- [ ] `GET /healthz`: health check for the ALB target group.

### Behaviour
- [ ] File IDs are random and can't be guessed (at least 128 bits, e.g. base62).
- [ ] Presigned URLs live a short time: upload about 15 min, download about 5 min *(proposed)*.
- [ ] Downloads send `Content-Disposition: attachment; filename="<original>"`.
- [ ] Every input is validated (size, expiry range, filename length and characters).
- [ ] Errors come back as consistent JSON with the right status codes.
- [ ] Logs are structured JSON (`log/slog`) with a request ID.
- [ ] Shuts down cleanly on SIGTERM, which ECS sends when it stops a task.
- [ ] Configured through environment variables; the DB password comes from Secrets Manager.
- [ ] Built with `net/http` or `chi`, and talks to Postgres through `pgx`.

## 3. Authentication: API keys

- [ ] Keys are random tokens shown once; only a SHA-256 hash is stored in Postgres.
- [ ] Keys are sent as `Authorization: Bearer <key>`.
- [ ] Keys can be created and revoked with an admin CLI subcommand or script (no signup flow).
- [ ] Each key can only see and delete its own files.
- [ ] Each key is rate-limited on upload creation, in the app or with WAF rate rules.
- [ ] Each key gets a storage quota *(proposed: optional)*.

## 4. Link controls

### Expiry
- [ ] The sender picks the expiry; default 24 h, maximum 7 days *(proposed)*.
- [ ] `expires_at` is stored in Postgres, and the API refuses downloads after it.
- [ ] The worker deletes the S3 object when a file expires and sets its status to `expired`.
- [ ] As a backstop, an S3 lifecycle rule deletes any object older than the maximum expiry plus one day.

### Size cap
- [ ] Maximum upload size is 100 MB *(proposed)*.
- [ ] S3 enforces the cap itself: use a **presigned POST** with a `content-length-range` condition. A presigned PUT can't enforce size.
- [ ] The API rejects a declared size over the cap before it signs anything.
- [ ] The worker checks the real object size with `HeadObject` and deletes anything that doesn't match.

## 5. Async processing: SQS and the Go worker

- [ ] An SQS queue for jobs, with a dead-letter queue (max receives about 5).
- [ ] Something starts each job: either S3 `ObjectCreated` events go to SQS, or the `/complete` endpoint sends a message. Pick one; S3 events are more SAA-relevant.
- [ ] **Hash job:** stream the object from S3, compute SHA-256, store it, and set status `ready`.
- [ ] **Expiry/cleanup:** a scheduled sweep finds expired files and deletes them. Use an EventBridge Scheduler message to SQS, or a ticker in the worker.
- [ ] **Orphan cleanup:** delete uploads that were never finished, after about 1 h.
- [ ] Jobs are idempotent, so a message handled twice causes no harm.
- [ ] The worker deletes a message only after it succeeds, and sets the visibility timeout longer than the slowest job.
- [ ] The worker runs as its own ECS Fargate service; it can run as a separate binary or as a subcommand of the same image.

## 6. Data: Postgres on RDS

- [ ] Versioned migrations (`golang-migrate`, `goose` or `tern`).
- [ ] `api_keys` table: id, name, key_hash, created_at, revoked_at.
- [ ] `files` table: id, api_key_id, s3_key, original_name, content_type, size_bytes, sha256, status (`pending`/`ready`/`expired`/`deleted`), created_at, expires_at.
- [ ] Indexes on `expires_at`, status and api_key_id.
- [ ] RDS sits in private subnets, uses encrypted storage and has automated backups. Single-AZ to save money, with Multi-AZ written down as the production choice.

## 7. Storage and encryption: S3 and KMS

- [ ] A private S3 bucket with all public access blocked.
- [ ] Default encryption is SSE-KMS with a customer-managed key, with S3 Bucket Keys on to cut KMS costs.
- [ ] The bucket policy denies non-TLS requests and requires SSE-KMS on uploads.
- [ ] The KMS key policy lets only the API and worker task roles use the key.
- [ ] CORS stays off unless a browser client is added later.
- [ ] The lifecycle rule from §4 is in place.

## 8. AWS infrastructure: Terraform

- [ ] Terraform state lives remotely: an S3 backend with locking (S3 native lockfile or DynamoDB).
- [ ] VPC with public and private subnets across two AZs.
- [ ] **No NAT Gateway:** use VPC endpoints (S3 gateway; ECR, SQS, KMS, Secrets Manager and CloudWatch Logs interface endpoints) or run tasks in public subnets for the demo.
- [ ] Security groups: ALB → API on the app port only; API and worker → RDS on 5432 only.
- [ ] An ECR repository with an image-scanning and lifecycle policy.
- [ ] An ECS cluster, plus Fargate task definitions and services for the API and the worker.
- [ ] Each task has a least-privilege role: the API can sign S3 URLs, use KMS and send to SQS; the worker can get/delete S3 objects, use KMS and receive/delete from SQS.
- [ ] An ALB with an HTTPS listener, an ACM certificate, a redirect from HTTP to HTTPS, and a target group that checks health on `/healthz`.
- [ ] A Route 53 record for `api.paulborrego.com`, with the zone delegated from the domain's registrar if needed.
- [ ] WAF on the ALB with AWS managed rules and a rate-based rule *(roadmap week 8: "basics")*.
- [ ] The database password lives in Secrets Manager and is passed into tasks as a secret.
- [ ] Every resource is tagged, and `terraform destroy` removes everything cleanly.

## 9. CI/CD: GitHub Actions

- [ ] AWS access through GitHub OIDC: an IAM role trusted only by this repo and branch, with no long-lived keys.
- [ ] Pull request workflow: `go vet`, `staticcheck`/`golangci-lint`, `go test ./...`, `terraform fmt -check`, `terraform validate` and `terraform plan`.
- [ ] Main branch workflow: build the image, tag it with the git SHA, push to ECR, `terraform apply` (or update the ECS service), then run migrations.
- [ ] Migrations run as a one-off ECS task, or when the app starts.

## 10. Observability

- [ ] Logs for both services go to CloudWatch Logs with a set retention period (e.g. 14 days).
- [ ] Metrics: request count and latency (the ALB has these built in), uploads created, files expired and worker errors.
- [ ] CloudWatch alarms on ALB 5xx rate, a non-empty DLQ, high queue age, high RDS CPU or low storage, and ECS tasks not running. Alarms notify through SNS to email.

## 11. Testing

- [ ] Table-driven unit tests for validation, ID and key generation, and expiry logic.
- [ ] Integration tests for the database layer against Postgres (testcontainers or Docker Compose).
- [ ] Local dev with Docker Compose: Postgres plus LocalStack (S3, SQS, KMS) or MinIO.
- [ ] An end-to-end smoke test script with `curl`: create an upload, upload the file, wait for `ready`, download it, compare the hash, delete it.

## 12. Cost and account hygiene

- [ ] A separate AWS account or dedicated IAM user, never root, with MFA on.
- [ ] Billing alerts and an AWS Budget set **before** building anything (roadmap week 3).
- [ ] No NAT Gateway (see §8).
- [ ] Run `terraform destroy` when you stop working, and write down how to bring everything back.

## 13. Documentation (roadmap week 12)

- [ ] A README with an architecture diagram covering the 7 components and their connections.
- [ ] `curl` examples for every endpoint.
- [ ] Notes on the threat model, including that this is server-side encryption, so AWS and the operator can decrypt, and that end-to-end encryption is future work.
- [ ] Notes on design decisions: why presigned POST, why SQS, how the NAT Gateway was avoided, and Single-AZ vs Multi-AZ.
- [ ] Check the comparison table against each project's own pages before quoting it (the roadmap's sources were AlternativeTo listings and one blog post).
- [ ] Résumé bullets rewritten around the project.

## Out of scope (possible later work)

- End-to-end client-side encryption, with the key in the URL fragment.
- A web UI or Go CLI client.
- Download limits ("burn after reading") and password-protected links.
- Multipart uploads for files over the size cap.
- Cognito user accounts.
- Kubernetes, the roadmap's optional stretch goal.

## Open questions

- Should the worker be started by S3 events, or by the `/complete` call? (§5)
- Are the proposed defaults OK: 100 MB cap, 24 h default and 7 day maximum expiry, 5 min download URLs?
- Is `encryptor-api/` related to this project, or a separate effort?

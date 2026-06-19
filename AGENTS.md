# Repository Guidelines

## Project Structure & Module Organization
This repository contains a Go backend and a Vue 3 frontend. `main.go` is the backend entrypoint, and server code lives under `internal/`: routing in `router`, HTTP handlers in `handler`, business logic in `services`, persistence in `db` and `store`, provider forwarding in `channel` and `proxy`, and helpers in `utils`. Backend tests use Go’s `_test.go` convention.

The frontend is in `web/src`: pages in `views`, reusable UI in `components`, API clients in `api`, helpers in `utils`, localization in `locales`, and CSS/images in `assets`. Documentation screenshots are in `screenshot/`. Deployment and configuration examples are in `Dockerfile`, `docker-compose.yml`, and `.env.example`.

## Build, Test, and Development Commands
- `make run`: installs frontend dependencies, builds `web/dist`, then starts the backend.
- `make dev`: runs the backend with Go race detection.
- `go test ./...`: runs all backend tests.
- `cd web && npm install`: installs frontend dependencies.
- `cd web && npm run dev`: starts the Vite frontend dev server.
- `cd web && npm run build`: runs `vue-tsc` and builds production assets.
- `cd web && npm run lint:check && npm run format:check`: checks ESLint/Prettier.
- `docker compose up -d`: runs the packaged service stack locally.

## Coding Style & Naming Conventions
Follow `.editorconfig`: UTF-8, LF line endings, final newline, 2-space indentation for TypeScript/Vue/YAML, tabs for Go and Makefiles. Run `gofmt` on Go changes. Keep Go package names short and lowercase; use PascalCase for exported identifiers. Frontend code uses TypeScript, Vue SFCs, ESLint, and Prettier. Use PascalCase component filenames and kebab-case component tags.

## Testing Guidelines
Add Go tests next to the package being tested with names like `manager_test.go` and functions such as `TestManagerBehavior`. Prefer table-driven tests for service, store, proxy, and channel logic. For frontend changes, run `npm run type-check`, `npm run lint:check`, and `npm run build`; there is no real frontend unit test suite yet.

## Commit & Pull Request Guidelines
Recent history uses concise messages, often Conventional Commit style such as `fix(security): ...`, `feat: ...`, `refactor(failover): ...`, and `docs: ...`. PRs should link an issue with `Closes #123`, describe the change, mark bug fix/new feature/other, confirm local testing, and update docs when behavior or configuration changes.

## Security & Configuration Tips
Do not commit secrets or local `.env` files. Use `.env.example` as the template for required configuration. Backup the database before running key migrations, for example `make migrate-keys ARGS="--from old-key --to new-key"`.

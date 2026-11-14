# FeiraNexus

Monorepo for the FeiraNexus e-commerce platform.

| Path | What it is |
|------|------------|
| [`fn_webserver/`](fn_webserver/) | **Go + Gin MVC REST API** — architecture, setup, and API docs live in its README |
| [`fn_webserver/docs/`](fn_webserver/docs/) | UML diagrams (class, ERD, activity, use case) as PlantUML `.puml` + rendered `.png`/`.svg` |
| [`.do/app.yaml`](.do/app.yaml) | DigitalOcean App Platform deployment spec |
| [`.github/workflows/ci-deploy.yml`](.github/workflows/ci-deploy.yml) | CI (`go vet`, `go test -race`) → deploy on push to `main` |

**Quick start** (details in [`fn_webserver/README.md`](fn_webserver/README.md)):

```bash
cd fn_webserver
./scripts/gen_env.sh   # generates .env with CSPRNG secrets
go mod tidy
make docker-up && make migrate
make run               # http://localhost:8080/healthz
```

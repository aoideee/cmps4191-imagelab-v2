# Load local database credentials when present. The file is intentionally
# untracked, so make itself must still work before a developer creates it.
-include .envrc

# ==================================================================================== #
# HELPERS
# ==================================================================================== #

## help: print this help message
.PHONY: help
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

.PHONY: confirm
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

.PHONY: check/db-dsn
check/db-dsn:
	@if [ -z "${GATEKEEPER_DB_DSN}" ]; then \
		echo 'GATEKEEPER_DB_DSN is not configured.'; \
		echo 'Copy .envrc.example to .envrc, replace YOUR_PASSWORD, then retry.'; \
		exit 1; \
	fi

# ==================================================================================== #
# DEVELOPMENT
# ==================================================================================== #

## run/api: run the cmd/api application
.PHONY: run/api
run/api: check/db-dsn
	go run ./cmd/api -db-dsn=${GATEKEEPER_DB_DSN} -job-delay=30s -simulation-duration=0s

.PHONY: run/api/logs
run/api/logs: check/db-dsn
	go run ./cmd/api -db-dsn=${GATEKEEPER_DB_DSN} -job-delay=0s -simulation-duration=10s -simulation-interval=200ms 2>&1 | tee EXPERIMENT-5_server_runs.log
	
.PHONY: run/frontend
run/frontend:
	npx http-server ./frontend -p 5500 -c-1

## db/psql: connect to the database using psql
.PHONY: db/psql
db/psql: check/db-dsn
	psql ${GATEKEEPER_DB_DSN}

## db/migrations/new name=$1: create a new database migration
.PHONY: db/migrations/new
db/migrations/new:
	@echo 'Creating migration files for ${name}...'
	migrate create -seq -ext=.sql -dir=./migrations ${name}

## db/migrations/up: apply all up database migrations
.PHONY: db/migrations/up
db/migrations/up: check/db-dsn confirm
	@echo 'Running up migrations...'
	migrate -path ./migrations -database ${GATEKEEPER_DB_DSN} up


## db/migrations/down: roll back the last database migration
.PHONY: db/migrations/down
db/migrations/down: check/db-dsn confirm
	@echo 'Rolling back last migration...'
	migrate -path ./migrations -database ${GATEKEEPER_DB_DSN} down 1

## db/migrations/force version=$1: force the migration version (to fix dirty state)
.PHONY: db/migrations/force
db/migrations/force: check/db-dsn
	@echo 'Forcing migration version to ${version}...'
	migrate -path ./migrations -database ${GATEKEEPER_DB_DSN} force ${version}

# ==================================================================================== #
# QUALITY CONTROL
# ==================================================================================== #

## tidy: format all .go files, and tidy and vendor module dependencies
.PHONY: tidy
tidy:
	@echo 'Tidying module dependencies...'
	go mod tidy
	@echo 'Verifying and vendoring module dependencies...'
	go mod verify
	go mod vendor
	@echo 'Formatting .go files...'
	go fmt ./...

## audit: run quality control checks
.PHONY: audit
audit:
	@echo 'Checking module dependencies...'
	go mod tidy -diff
	go mod verify
	@echo 'Vetting code...'
	go vet ./...
	go tool staticcheck ./...
	@echo 'Running tests...'
	go test -race -vet=off ./...

# ==================================================================================== #
# BUILD
# ==================================================================================== #

## build/api: build the cmd/api application
.PHONY: build/api
build/api:
	@echo 'Building cmd/api...'
	go build -ldflags="-s" -o=./bin/api ./cmd/api
	GOOS=linux GOARCH=amd64 go build -ldflags="-s" -o=./bin/linux_amd64/api ./cmd/api

# ==================================================================================== #
# PRODUCTION
# ==================================================================================== #

production_host_ip = "your.ip.address"

## production/connect: connect to the production server
.PHONY: production/connect
production/connect:
	ssh gatekeeper@${production_host_ip}

## production/deploy/api: deploy the api to production
.PHONY: production/deploy/api
production/deploy/api:
	rsync -P ./bin/linux_amd64/api gatekeeper@${production_host_ip}:~
	rsync -rP --delete ./migrations gatekeeper@${production_host_ip}:~
	rsync -P ./remote/production/api.service gatekeeper@${production_host_ip}:~
	rsync -P ./remote/production/Caddyfile gatekeeper@${production_host_ip}:~
	ssh -t gatekeeper@${production_host_ip} '\''\
		migrate -path ~/migrations -database $$GATEKEEPER_DB_DSN up \
		&& sudo mv ~/api.service /etc/systemd/system/ \
		&& sudo systemctl enable api \
		&& sudo systemctl restart api \
		&& sudo mv ~/Caddyfile /etc/caddy/ \
		&& sudo systemctl reload caddy \
	'\''

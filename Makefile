run:
	go run cmd/main.go
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/api cmd/main.go
test:
	go test -v ./...
mod-tidy:
	go mod tidy
golint:
	golangci-lint run --timeout 5m

scan:
	@echo "Running security scan"
	gosec ./...

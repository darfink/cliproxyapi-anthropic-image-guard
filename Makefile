.PHONY: test vet build smoke bench

test:
	go test -race -tags debug ./...

vet:
	go vet ./...

build:
	go build -tags debug -buildmode=c-shared -o dist/anthropic-image-guard.dylib .

smoke: build
	node tools/smoke-host.mjs
	node tools/smoke-host.mjs --large

bench:
	go test -tags debug ./internal/guard -run '^$$' -bench BenchmarkWarmReplay -benchmem

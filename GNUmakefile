default: test

build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run

testacc:
	TF_ACC=1 go test ./... -run TestAcc -v -timeout 60m

docs:
	go tool tfplugindocs generate --provider-name proxcenter

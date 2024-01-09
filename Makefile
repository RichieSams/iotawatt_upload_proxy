default: image

ifeq ($(OS),Windows_NT)
export SHELL=cmd
DETECTED_OS=windows
EXE=.exe
else
DETECTED_OS=$(shell uname -s | tr '[:upper:]' '[:lower:]')
endif

ifeq ($(DETECTED_OS),windows)
TAG:=$(shell git describe --tags --dirty 2> nul || echo -n v0.0.0)
else
TAG:=$(shell git describe --tags --dirty 2>/dev/null || echo -n v0.0.0)
endif


.PHONY: vendor build

###########
# Building the binary locally
###########

build:
	go build -o build/$(DETECTED_OS)/iup$(EXE)


###########
# Creating a docker image
###########

image:
ifeq ($(DETECTED_OS),windows)
	cmd /C "set GORELEASER_CURRENT_TAG=$(TAG)&&goreleaser release --snapshot --clean"
else
	GORELEASER_CURRENT_TAG=$(TAG) goreleaser release --snapshot --clean
endif


###########
# Local testing with docker-compose
###########

run:
	docker run --rm -it \
		-e IUP_UPSTREAM=http://192.168.68.107:8428 \
		-p 8888:8888 \
		ghcr.io/richiesams/iotawatt_upload_proxy:$(TAG)


###########
# Unit / functional testing
###########

test:
	go test -cover ./...


###########
# Creating a release in CI
###########

release:
	goreleaser release --clean


###########
# Miscellaneous
###########

# Run golangci-lint on all our code
lint:
	golangci-lint run

# If you update / add any new dependencies, re-run this command to re-generate the vendor folder
vendor:
	go mod tidy
	go mod vendor

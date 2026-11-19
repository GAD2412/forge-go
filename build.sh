#!/bin/sh
# Build the forge: a static linux/amd64 binary for the grader, and a native one for local runs.
set -e
cd "$(dirname "$0")/src"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-s -w" -o ../bin/forge-linux-x64 .
go build -trimpath -buildvcs=false -o "../bin/forge-$(go env GOOS)-$(go env GOARCH | sed 's/amd64/x64/')" .

#!/usr/bin/env bash

CGO_ENABLED=0 go build \
    -ldflags "\
        -X github.com/grafana/mimir/pkg/util/version.Branch=$(git rev-parse --abbrev-ref HEAD) \
        -X github.com/grafana/mimir/pkg/util/version.Revision=$(git rev-parse --short HEAD) \
        -X github.com/grafana/mimir/pkg/util/version.Version=$(cat "./VERSION" 2> /dev/null) \
        -extldflags \"-static\" -s -w" \
    -tags netgo \
    -o ./dist/mimir \
    ./cmd/mimir

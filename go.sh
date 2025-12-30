#!/bin/bash
# Optional override: go run . -port 8099

go run ./cmd/radar -config config.yaml -watchlist watchlist.yaml

#!/usr/bin/env bash

set -e

# Imports needed for compilation only; their Go code comes from the vendored
# l8types and l8common dependencies, not from local generation.
wget https://raw.githubusercontent.com/saichler/l8types/refs/heads/main/proto/api.proto
wget https://raw.githubusercontent.com/saichler/l8common/refs/heads/main/proto/l8common.proto

# Use the protoc image to run protoc.sh and generate the bindings.
docker run --user "$(id -u):$(id -g)" -e PROTO=aimask.proto --mount type=bind,source="$PWD",target=/home/proto/ -i registry.layer-8.dev:44443/library/protoc:latest
docker run --user "$(id -u):$(id -g)" -e PROTO=l8ai.proto --mount type=bind,source="$PWD",target=/home/proto/ -i registry.layer-8.dev:44443/library/protoc:latest

rm api.proto l8common.proto

# Now move the generated bindings to the go/types directory and clean up
rm -rf ../go/types
mkdir -p ../go/types
rm -rf ./types/l8common ./types/l8api
mv ./types/* ../go/types/.
rm -rf ./types
rm -rf *.rs

cd ../go
find ./types -name "*.go" -type f -exec sed -i 's|"./types/l8api"|"github.com/saichler/l8types/go/types/l8api"|g' {} +
find ./types -name "*.go" -type f -exec sed -i 's|"./types/l8common"|"github.com/saichler/l8common/go/types/l8common"|g' {} +

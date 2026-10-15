#!/usr/bin/env bash
# Runs both code generators: the protobuf stubs, then the GraphQL code.
#
# Generation is a host step, not a Bazel action. gqlgen resolves Go types by
# shelling out to the Go toolchain through go/packages, which does not work inside
# the Bazel sandbox, and upstream does not have a landed fix for that
# (99designs/gqlgen#3181). So both outputs are committed and this script is run by
# hand after editing a schema or a proto. `go build` and `bazel build` still
# compile the result, so a broken generation fails the build.
set -euo pipefail

cd "$(dirname "$0")/.."

./hack/gen-proto.sh

# The config lives in graph/, so it is passed explicitly rather than relying on
# gqlgen's search of the working directory.
echo "generating GraphQL code"
GOWORK=off GOFLAGS=-mod=mod go run github.com/99designs/gqlgen --config graph/gqlgen.yml generate

# The generator adds gqlgen to go.mod when skip_mod_tidy is off; with it on, tidy
# here keeps the committed go.mod and go.sum honest.
GOWORK=off GOFLAGS=-mod=mod go mod tidy

echo "done. review the generated diff before committing."

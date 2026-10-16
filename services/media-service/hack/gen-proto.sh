#!/usr/bin/env bash
# Regenerates the gRPC stubs in gen/ from the contracts in proto/.
#
# The generated files are committed so that building a service needs nothing but
# Go. Re-run this after editing a .proto file.
#
# Requires protoc plus the two Go plugins on PATH:
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
set -euo pipefail

cd "$(dirname "$0")/.."

# protoc ships the well-known types (timestamp.proto and friends) in its own
# include directory. A normal install has it next to the binary; a manually
# unpacked release needs PROTOC_INCLUDE pointing at it.
if [[ -z "${PROTOC_INCLUDE:-}" ]]; then
  protoc_include="$(dirname "$(command -v protoc)")/../include"
  if [[ -f "${protoc_include}/google/protobuf/timestamp.proto" ]]; then
    PROTOC_INCLUDE="${protoc_include}"
  fi
fi

args=(-I proto)
# -I may only be passed when the directory actually exists.
[[ -n "${PROTOC_INCLUDE:-}" ]] && args+=(-I "${PROTOC_INCLUDE}")

protoc "${args[@]}" \
  --go_out=. --go_opt=module=media-service \
  --go-grpc_out=. --go-grpc_opt=module=media-service \
  proto/media/v1/media.proto

echo "generated gen/mediapb"

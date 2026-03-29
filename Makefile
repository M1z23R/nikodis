.PHONY: proto

proto:
	@mkdir -p pkg/gen
	protoc \
		--proto_path=proto \
		--go_out=pkg/gen --go_opt=paths=source_relative \
		--go-grpc_out=pkg/gen --go-grpc_opt=paths=source_relative \
		nikodis.proto

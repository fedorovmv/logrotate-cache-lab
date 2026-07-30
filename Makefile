.PHONY: test vet build docker-build docker-smoke

IMAGE ?= logrotate-cache-lab:dev

test:
	go test ./... -count=1

vet:
	go vet ./...

build:
	go build -o bin/loglab ./cmd/loglab

docker-build:
	docker build -t $(IMAGE) .

docker-smoke: docker-build
	@result_dir=$$(mktemp -d); \
	volume_name=loglab-smoke-$$$$; \
	docker volume create $$volume_name >/dev/null; \
	trap 'docker volume rm -f "$$volume_name" >/dev/null 2>&1; rm -rf "$$result_dir"' EXIT; \
	docker run --rm --mount source=$$volume_name,target=/var/log/loglab \
		-v $$result_dir:/results $(IMAGE) run --strategy rename-reopen \
		--run-id smoke --max-file-bytes 2097152 --rotations 1 \
		--bytes-per-second 4194304 --resident-bytes 8388608 >/dev/null; \
	test -s $$result_dir/summary.json

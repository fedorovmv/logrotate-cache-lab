.PHONY: test vet build build-go build-cpp docker-build docker-smoke compare memory-sweep kind-sweep

IMAGE ?= logrotate-cache-lab:dev
CXX ?= c++
CXXFLAGS ?= -std=c++17 -O2 -pthread

test:
	go test ./... -count=1

vet:
	go vet ./...

build: build-go build-cpp

build-go:
	mkdir -p bin
	go build -o bin/loglab ./cmd/loglab

build-cpp:
	mkdir -p bin
	$(CXX) $(CXXFLAGS) -o bin/logwriter-cpp cpp/writer/main.cc

docker-build:
	docker build -t $(IMAGE) .

docker-smoke: docker-build
	@result_dir=$$(mktemp -d); \
	chmod 0777 $$result_dir; \
	volume_name=loglab-smoke-$$$$; \
	docker volume create $$volume_name >/dev/null; \
	trap 'docker volume rm -f "$$volume_name" >/dev/null 2>&1; rm -rf "$$result_dir"' EXIT; \
	docker run --rm --mount source=$$volume_name,target=/var/log/loglab \
		-v $$result_dir:/results $(IMAGE) run --strategy rename-reopen \
		--run-id smoke --max-file-bytes 2097152 --rotations 1 \
		--bytes-per-second 4194304 --resident-bytes 8388608 >/dev/null; \
	test -s $$result_dir/summary.json

compare:
	./scripts/compare.sh

memory-sweep:
	./scripts/memory-sweep.sh

kind-sweep:
	./scripts/kind-sweep.sh --quick

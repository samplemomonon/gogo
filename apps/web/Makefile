# Nx runs gateway targets from this project directory. The monorepo Makefile
# owns cross-module orchestration and remains the single implementation.
.DEFAULT_GOAL := build

.PHONY: build bundle test lint format format-check tidy run generate buf-generate check \
	arch-graph docker-build up down k8s-build

build bundle:
	$(MAKE) -C ../.. build

test:
	$(MAKE) -C ../.. test

lint:
	$(MAKE) -C ../.. lint

format:
	$(MAKE) -C ../.. fmt

format-check check:
	$(MAKE) -C ../.. check

tidy:
	$(MAKE) -C ../.. tidy

run:
	$(MAKE) -C ../.. run

generate:
	$(MAKE) -C ../.. buf-generate

buf-generate:
	$(MAKE) -C ../.. buf-generate

arch-graph:
	$(MAKE) -C ../.. arch-graph

docker-build:
	$(MAKE) -C ../.. docker-build

up down k8s-build:
	$(MAKE) -C ../.. $@

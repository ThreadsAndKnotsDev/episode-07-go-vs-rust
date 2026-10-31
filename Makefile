.PHONY: help setup bench bench-matrix bench-quick report clean go-dev rust-dev

help:
	@$(MAKE) -C ep07-go-vs-rust-benchmark help

setup:
	@$(MAKE) -C ep07-go-vs-rust-benchmark setup

bench:
	@$(MAKE) -C ep07-go-vs-rust-benchmark bench

bench-matrix:
	@$(MAKE) -C ep07-go-vs-rust-benchmark bench-matrix

bench-quick:
	@$(MAKE) -C ep07-go-vs-rust-benchmark bench-quick

report:
	@$(MAKE) -C ep07-go-vs-rust-benchmark report

clean:
	@$(MAKE) -C ep07-go-vs-rust-benchmark clean

go-dev:
	@$(MAKE) -C ep07-go-vs-rust-benchmark go-dev

rust-dev:
	@$(MAKE) -C ep07-go-vs-rust-benchmark rust-dev
